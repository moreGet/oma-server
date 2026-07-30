package httpin

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 요청 본문 gzip 수용(클라이언트 압축 스펙 §1).
//
// 에이전트는 도구를 호출할 때마다 대화 **전문**을 다시 보낸다. 한 턴에 도구 왕복이 10~30 회면
// 같은 이력이 그만큼 반복 전송되고, 이력에는 도구가 읽은 소스 원문이 그대로 들어 있어 gzip 이
// 특히 잘 듣는다(클라이언트 실측: 64KB→15KB, 250KB→55KB, 650KB→131KB — 76~80% 감소).
//
// **nginx 로는 해결되지 않는다.** ngx_http_gunzip_module 은 이름과 달리 *업스트림이 준 응답*을
// 푸는 모듈이라 요청 본문에는 관여하지 않는다. 요청 압축은 반드시 애플리케이션이 처리해야 한다.
//
// 처리 위치를 미들웨어가 아니라 본문 디코더(decodeJSON)로 잡은 이유:
//   - 이 서버는 에러 envelope 이 둘이다(Handle 의 flat, HandleAgent 의 중첩). 디코더에서
//     AppError 를 반환하면 각 라우트가 이미 쓰는 envelope 으로 자동 직렬화된다.
//   - 해제 후 상한을 라우트별 maxBytes 로 그대로 쓸 수 있다(agent/chat 32MiB, 그 외 1MiB).
//     미들웨어는 라우트를 모르므로 전역 상수를 하나 더 만들어야 한다.
//
// JSON 본문을 읽는 경로는 전부 decodeJSON 을 지나므로 적용 범위는 "모든 JSON 엔드포인트"다.
// multipart(첨부 업로드)는 JSON 이 아니라 이 경로를 타지 않으며, 별도로 415 를 반환한다.

// 지원 인코딩. deflate/br 은 스펙상 선택이라 구현하지 않는다(요청 시 415).
const encodingGzip = "gzip"

var (
	// errMalformedBody 는 Content-Encoding: gzip 인데 본문이 gzip 이 아니거나 스트림이 깨진 경우다.
	errMalformedBody = errors.New("malformed compressed body")
	// errPayloadTooLarge 는 해제 결과가 상한을 넘은 경우다(zip bomb 방어).
	errPayloadTooLarge = errors.New("decompressed body exceeds limit")
)

// requestBodyReader 는 Content-Encoding 에 따라 요청 본문 리더를 만든다.
//
//   - 헤더 없음 / identity → 원본 본문(하위 호환: 종전 동작과 바이트 동일)
//   - gzip                → 스트리밍 해제 리더(해제 누적 상한 limit)
//   - 그 외               → 415
//
// limit 은 **압축 전 바이트와 해제 후 바이트 양쪽**에 걸린다. 해제를 전량 버퍼링하지 않고
// 스트리밍으로 처리하되 누적 바이트를 세는 것이 핵심이다 — 상한 없이 전량 해제하면 작은
// 요청 하나로 서버 메모리를 고갈시킬 수 있다.
func requestBodyReader(w http.ResponseWriter, r *http.Request, limit int64) (io.ReadCloser, error) {
	// 압축 여부와 무관하게 네트워크에서 읽는 바이트를 먼저 제한한다.
	body := http.MaxBytesReader(w, r.Body, limit)

	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		return body, nil
	case encodingGzip:
		zr, err := gzip.NewReader(body)
		if err != nil {
			// gzip 헤더는 즉시 읽히므로 형식 오류가 여기서 걸린다.
			return nil, malformedBodyErr(err)
		}
		return &limitedDecompressor{zr: zr, limit: limit}, nil
	default:
		return nil, unsupportedEncodingErr(enc)
	}
}

// limitedDecompressor 는 해제 누적 바이트가 limit 을 넘으면 errPayloadTooLarge 로 끊는 리더다.
// gzip 스트림 자체의 오류(체크섬·중도 절단)는 errMalformedBody 로 감싸 디코더가 구분할 수 있게 한다.
type limitedDecompressor struct {
	zr    *gzip.Reader
	limit int64
	read  int64 // 지금까지 해제한 누적 바이트
}

func (d *limitedDecompressor) Read(p []byte) (int, error) {
	// 초과 상태로는 절대 재진입하지 않는다. 이 가드가 없으면 아래 room 이 0 이 되어
	// Read 가 (0, nil) 을 무한히 돌려주고 호출자가 영원히 되돌아온다(무한 루프).
	if d.read > d.limit {
		return 0, errPayloadTooLarge
	}
	// 상한보다 1바이트 더 읽어보고 초과를 판정한다(정확히 상한이면 통과시킨다).
	// 위 가드 덕분에 room 은 항상 1 이상이라 빈 슬라이스로 읽는 일이 없다.
	if room := d.limit - d.read + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := d.zr.Read(p)
	d.read += int64(n)
	if d.read > d.limit {
		return 0, errPayloadTooLarge
	}
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	// 압축 전 바이트가 상한을 넘은 경우는 "깨진 본문"이 아니라 "과대 본문"이다.
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return n, errPayloadTooLarge
	}
	return n, malformedBodyErr(err)
}

func (d *limitedDecompressor) Close() error { return d.zr.Close() }

// malformedBodyErr 는 원인을 감싼 errMalformedBody 를 만든다(원인은 로그용, 응답에 노출 안 됨).
func malformedBodyErr(cause error) error {
	return fmt.Errorf("%w: %v", errMalformedBody, cause)
}

// unsupportedEncodingErr 는 415 AppError 를 만든다.
// agent 계열 라우트는 클라이언트 계약대로 중첩 envelope 의 unsupported_encoding 코드로 나간다.
func unsupportedEncodingErr(enc string) *AppError {
	return ErrUnsupportedMediaType("unsupported content encoding: " + enc).
		WithAgentCode(agentCodeUnsupportedEncoding)
}

// bodyReadErrToHTTP 는 본문 읽기 중 발생한 에러를 AppError 로 매핑한다.
// 압축 관련 sentinel 이 아니면 nil 을 반환해 호출부가 기존 400 처리를 하게 한다.
func bodyReadErrToHTTP(err error) *AppError {
	switch {
	case errors.Is(err, errPayloadTooLarge):
		return ErrPayloadTooLarge("request body exceeds size limit").
			WithAgentCode(agentCodePayloadTooLarge).WithCause(err)
	case errors.Is(err, errMalformedBody):
		return ErrBadRequest("malformed compressed request body").
			WithAgentCode(agentCodeMalformedBody).WithCause(err)
	default:
		return nil
	}
}

// requireIdentityEncoding 은 JSON 이 아닌 본문(multipart 등)에 압축이 걸려 오면 415 를 반환한다.
// 이 경로는 스트리밍 해제를 지원하지 않으므로, 조용히 깨진 파싱 오류를 내는 대신 명시적으로 거절한다.
func requireIdentityEncoding(r *http.Request) error {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return nil
	default:
		return unsupportedEncodingErr(r.Header.Get("Content-Encoding"))
	}
}

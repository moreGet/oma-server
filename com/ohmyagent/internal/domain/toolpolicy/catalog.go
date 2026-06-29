package toolpolicy

// 이 파일은 클라이언트(OhMyAgent.AiAgent.Client)가 노출하는 도구 카탈로그의
// 서버측 단일 진실원(source of truth)이다. 어드민이 허용/차단 도구를 자유 문자열로
// 입력하다 생기는 오타를 막기 위해, 도구명을 상수·고정 카탈로그(칩)로 제공한다.
//
// sync with client App.xaml.cs tools[]
// 도구를 추가/제거하면 (1) 아래 상수·ClientTools, (2) 마이그레이션
// 00020_seed_tool_catalog.sql 의 tool_catalog 시드를 함께 갱신한다.
//
// 출처: 각 ITool.Name (ToolRegistry → ToolSchema.name == 와이어 name == 정책 매칭 키).
// 정책 매칭은 서버가 내려준 문자열을 대소문자 구분하여 그대로 비교하므로 전부 소문자 snake_case.

// 도구 카테고리(어드민 UI 그룹핑/칩 분류용).
const (
	CategoryShell    = "셸"
	CategoryFile     = "파일"
	CategorySystem   = "시스템"
	CategoryDocument = "문서·데이터"
)

// 도구명 상수 — 서버 코드에서 도구명을 참조할 때 리터럴 대신 사용해 오타를 컴파일타임에 차단한다.
const (
	ToolRunCommand            = "run_command"
	ToolReadFile              = "read_file"
	ToolWriteFile             = "write_file"
	ToolEditFile              = "edit_file"
	ToolListDirectory         = "list_directory"
	ToolGlob                  = "glob"
	ToolGrep                  = "grep"
	ToolCreateDirectory       = "create_directory"
	ToolMove                  = "move"
	ToolCopy                  = "copy"
	ToolDelete                = "delete"
	ToolGetEnvironment        = "get_environment"
	ToolClipboardRead         = "clipboard_read"
	ToolClipboardWrite        = "clipboard_write"
	ToolListProcesses         = "list_processes"
	ToolListProcessesMemoryKB = "list_processes_memory_kb"
	ToolStartProcess          = "start_process"
	ToolKillProcess           = "kill_process"
	ToolHTTPFetch             = "http_fetch"
	ToolScreenshot            = "screenshot"
	ToolReadCSV               = "read_csv"
	ToolWriteCSV              = "write_csv"
	ToolReadExcel             = "read_excel"
	ToolWriteExcel            = "write_excel"
	ToolReadPDF               = "read_pdf"
	ToolReadDocument          = "read_document"
)

// CatalogEntry 는 카탈로그 도구 1건이다(이름 + 카테고리 + 노출 순서).
type CatalogEntry struct {
	Name     string // 와이어 도구명(소문자 snake_case)
	Category string // CategoryShell|File|System|Document
}

// ClientTools 는 클라이언트가 노출하는 26개 도구를 App.xaml.cs tools[] 등록(노출) 순서대로 담는다.
// 어드민 UI 는 이 목록으로 선택 칩을 렌더하고, 정책(enabled/disabled)은 이 이름들로만 구성한다.
var ClientTools = []CatalogEntry{
	{ToolRunCommand, CategoryShell},
	{ToolReadFile, CategoryFile},
	{ToolWriteFile, CategoryFile},
	{ToolEditFile, CategoryFile},
	{ToolListDirectory, CategoryFile},
	{ToolGlob, CategoryFile},
	{ToolGrep, CategoryFile},
	{ToolCreateDirectory, CategoryFile},
	{ToolMove, CategoryFile},
	{ToolCopy, CategoryFile},
	{ToolDelete, CategoryFile},
	{ToolGetEnvironment, CategorySystem},
	{ToolClipboardRead, CategorySystem},
	{ToolClipboardWrite, CategorySystem},
	{ToolListProcesses, CategorySystem},
	{ToolListProcessesMemoryKB, CategorySystem},
	{ToolStartProcess, CategorySystem},
	{ToolKillProcess, CategorySystem},
	{ToolHTTPFetch, CategorySystem},
	{ToolScreenshot, CategorySystem},
	{ToolReadCSV, CategoryDocument},
	{ToolWriteCSV, CategoryDocument},
	{ToolReadExcel, CategoryDocument},
	{ToolWriteExcel, CategoryDocument},
	{ToolReadPDF, CategoryDocument},
	{ToolReadDocument, CategoryDocument},
}

// ClientToolNames 는 ClientTools 의 도구명만 등록 순서대로 추린 슬라이스다.
func ClientToolNames() []string {
	names := make([]string, 0, len(ClientTools))
	for _, t := range ClientTools {
		names = append(names, t.Name)
	}
	return names
}

// IsKnownTool 은 도구명이 카탈로그에 존재하는지 반환한다(정책 입력 검증용).
func IsKnownTool(name string) bool {
	for _, t := range ClientTools {
		if t.Name == name {
			return true
		}
	}
	return false
}

package messagingapp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalBroadcaster 는 로컬 브로드캐스터가 허브의 해당 멤버 연결로만 전달하는지 검증한다.
func TestLocalBroadcaster(t *testing.T) {
	hub := NewHub()
	bc := NewLocalBroadcaster(hub)

	c1 := &Client{MemberID: "u1", Send: make(chan []byte, 4)}
	hub.Register(c1)
	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 4)}
	hub.Register(c2)

	bc.Broadcast([]string{"u1"}, []byte("hi"))

	select {
	case got := <-c1.Send:
		assert.Equal(t, "hi", string(got))
	default:
		t.Fatal("u1 이 받지 못함")
	}
	select {
	case <-c2.Send:
		t.Fatal("u2(대상 아님)가 받으면 안 됨")
	default:
	}
	require.NoError(t, bc.Close())
}

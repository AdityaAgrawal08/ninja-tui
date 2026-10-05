package sender

import (
	"errors"
	"net/textproto"
	"testing"
)

func TestIsTemporary(t *testing.T) {
	if !IsTemporary(&textproto.Error{Code: 451, Msg: "queue full, try again later"}) {
		t.Fatal("451 should be temporary")
	}
	if IsTemporary(&textproto.Error{Code: 550, Msg: "no thanks"}) {
		t.Fatal("550 should not be temporary")
	}
	if IsTemporary(errors.New("plain")) {
		t.Fatal("non-SMTP error should not be temporary")
	}
}

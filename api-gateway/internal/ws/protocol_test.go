package ws

import (
	"encoding/binary"
	"testing"
)

func TestEncodeMarketFrame(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	frame := encodeMarketFrame(tagBars, "BTCUSDT", payload)

	if frame[0] != tagBars {
		t.Fatalf("tag = %x, want %x", frame[0], tagBars)
	}
	keyLen := binary.BigEndian.Uint16(frame[1:3])
	if int(keyLen) != len("BTCUSDT") {
		t.Fatalf("key len = %d, want %d", keyLen, len("BTCUSDT"))
	}
	gotKey := string(frame[3 : 3+keyLen])
	if gotKey != "BTCUSDT" {
		t.Fatalf("key = %q, want BTCUSDT", gotKey)
	}
	gotPayload := frame[3+keyLen:]
	if string(gotPayload) != string(payload) {
		t.Fatalf("payload = %x, want %x", gotPayload, payload)
	}
}

func TestLastToken(t *testing.T) {
	cases := map[string]string{
		"helm.events.abc-123":     "abc-123",
		"trade.filled.acct-9":     "acct-9",
		"portfolio.synced.acct-9": "acct-9",
		"noseparator":             "noseparator",
		"a.b.c.d":                 "d",
	}
	for in, want := range cases {
		if got := lastToken(in); got != want {
			t.Errorf("lastToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAccountCh(t *testing.T) {
	for _, ch := range []string{"helm", "trade", "portfolio"} {
		if !isAccountCh(ch) {
			t.Errorf("isAccountCh(%q) = false, want true", ch)
		}
	}
	for _, ch := range []string{"bars", "signals", ""} {
		if isAccountCh(ch) {
			t.Errorf("isAccountCh(%q) = true, want false", ch)
		}
	}
}

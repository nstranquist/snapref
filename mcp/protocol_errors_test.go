package browsermcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeReportsProtocolErrorsAndContinues(t *testing.T) {
	for _, tt := range []struct {
		frame string
		code  int
	}{
		{`{`, -32700}, {`null`, -32600}, {`[]`, -32600},
		{`{"jsonrpc":"2.0","id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":null,"id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"","id":1}`, -32601},
		{`{"method":"ping","id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":1,"id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"ping","id":true}`, -32600},
		{`{"jsonrpc":"2.0","method":"ping","id":{},"params":{}}`, -32600},
		{`{"jsonrpc":"2.0","method":"ping","id":"a","params":false}`, -32602},
	} {
		t.Run(tt.frame, func(t *testing.T) {
			in := tt.frame + "\n" + `{"jsonrpc":"2.0","method":"ping"}` + "\n" + `{"jsonrpc":"2.0","method":"ping","id":2}` + "\n"
			var out bytes.Buffer
			if err := Serve(strings.NewReader(in), &out, Options{Driver: &fakeDriver{}}); err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(&out)
			var first, second rpcResponse
			if err := dec.Decode(&first); err != nil {
				t.Fatal(err)
			}
			if first.Error == nil || first.Error.Code != tt.code || len(first.ID) == 0 {
				t.Fatalf("first=%+v", first)
			}
			if err := dec.Decode(&second); err != nil {
				t.Fatal(err)
			}
			if second.Error != nil || string(second.ID) != "2" {
				t.Fatalf("second=%+v", second)
			}
			if dec.Decode(&rpcResponse{}) == nil {
				t.Fatal("notification produced a response")
			}
		})
	}
}

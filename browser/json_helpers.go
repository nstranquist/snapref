package browser

import "encoding/json"

// jsonUnmarshalLoose is a thin wrapper around json.Unmarshal that's used
// by helpers in this package so we can pivot the implementation if we
// need lenient JSON later. Today it's identical to json.Unmarshal.
func jsonUnmarshalLoose(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

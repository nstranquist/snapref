package skills

import "encoding/json"

// jsonRoundTrip marshals src and unmarshals into dst. Used by tests to
// project a canned `any` value into a strongly-typed output struct, the
// same way chromedp.Evaluate writes into the caller's pointer.
func jsonRoundTrip(src, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

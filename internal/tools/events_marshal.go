package tools

// jsonMarshalImpl is the actual implementation of jsonMarshal. It
// uses encoding/json via a small adapter to keep the test file's
// imports tidy.
import "encoding/json"

func jsonMarshalImpl(v map[string]any) ([]byte, error) {
	return json.Marshal(v)
}
package web

import "encoding/json"

func jsonUnmarshalImpl(b []byte, out any) error { return json.Unmarshal(b, out) }

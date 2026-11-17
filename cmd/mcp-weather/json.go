package main

import (
	"encoding/json"
	"fmt"
)

func jsonUnmarshal(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("bad JSON from upstream: %v", err)
	}
	return nil
}

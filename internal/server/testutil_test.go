package server

import (
	"encoding/json"

	"github.com/linquin/longcat2api/internal/config"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func configDefaultForTest() *config.Config { return config.Default() }

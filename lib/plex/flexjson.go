package plex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/LukeHagar/plexgo/models/components"
)

// Plex PMS often returns 0/1 where clients expect JSON booleans. plexgo uses *bool fields
// that cannot decode numbers, so we parse library responses with map[string]any and helpers.

func decodeJSONObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	return root, nil
}

func flexString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return anyToString(v)
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return fmt.Sprint(x)
	}
}

func flexInt64Ptr(m map[string]any, key string) *int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	i, ok := anyToInt64(v)
	if !ok {
		return nil
	}
	if i == 0 {
		return nil
	}
	return &i
}

func flexInt64Required(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	i, ok := anyToInt64(v)
	if !ok {
		return 0
	}
	return i
}

func anyToInt(v any) (int, bool) {
	i64, ok := anyToInt64(v)
	if !ok {
		return 0, false
	}
	if i64 < int64(math.MinInt) || i64 > int64(math.MaxInt) {
		return 0, false
	}
	return int(i64), true
}

func anyToInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			f, err2 := x.Float64()
			if err2 != nil {
				return 0, false
			}
			return int64(f), true
		}
		return i, true
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case string:
		i, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, false
		}
		return i, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func anyToFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func asMapSlice(v any) []map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return []map[string]any{m}
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func itemsFromMediaContainer(mc map[string]any) []map[string]any {
	if mc == nil {
		return nil
	}
	if d := mc["Directory"]; d != nil {
		return asMapSlice(d)
	}
	if d := mc["Metadata"]; d != nil {
		return asMapSlice(d)
	}
	return nil
}

func flexGenreTags(m map[string]any) []components.Tag {
	// Plex JSON uses capital "Genre" for the array
	var raw any
	var ok bool
	if raw, ok = m["Genre"]; !ok {
		raw, ok = m["genre"]
	}
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []components.Tag
	for _, e := range arr {
		gm, ok := e.(map[string]any)
		if !ok {
			continue
		}
		tag := flexString(gm, "tag")
		if tag == "" {
			continue
		}
		out = append(out, components.Tag{Tag: tag})
	}
	return out
}

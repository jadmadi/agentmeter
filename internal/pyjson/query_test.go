// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"encoding/json"
	"testing"
)

func queryFixture() map[string]any {
	return map[string]any{
		"schemaVersion": int64(3),
		"meta": map[string]any{
			"timing": map[string]any{"plausible": int64(7), "slowRate": 0.0},
			"label":  "cmd",
		},
		"steps": []any{
			[]any{int64(1700000000), int64(0), "answer"},
			[]any{int64(1700000001), 1.5, nil},
		},
		"empty":    []any{},
		"flag":     true,
		"jsonNum":  json.Number("2.5"),
		"overflow": uint64(18446744073709551615),
	}
}

func TestQuery(t *testing.T) {
	data := queryFixture()
	cases := []struct {
		path []string
		want any
		ok   bool
	}{
		{[]string{"schemaVersion"}, int64(3), true},
		{[]string{"meta", "label"}, "cmd", true},
		{[]string{"meta", "timing", "plausible"}, int64(7), true},
		{[]string{"steps", "1", "1"}, 1.5, true},
		{[]string{"steps", "2"}, nil, false},
		{[]string{"steps", "-1"}, nil, false},
		{[]string{"steps", "x"}, nil, false},
		{[]string{"meta", "missing"}, nil, false},
		{[]string{"missing", "deep"}, nil, false},
		{[]string{"schemaVersion", "deeper"}, nil, false},
		{[]string{}, data, true},
	}
	for _, test := range cases {
		got, ok := Query(data, test.path...)
		if ok != test.ok {
			t.Errorf("Query(%v) ok = %v, want %v", test.path, ok, test.ok)
			continue
		}
		if test.ok && len(test.path) > 0 && got != test.want {
			t.Errorf("Query(%v) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestQueryTyped(t *testing.T) {
	data := queryFixture()
	if got, ok := QueryString(data, "meta", "label"); !ok || got != "cmd" {
		t.Errorf("QueryString = %q, %v", got, ok)
	}
	if _, ok := QueryString(data, "meta", "timing"); ok {
		t.Error("QueryString accepted an object")
	}
	if got, ok := QueryFloat(data, "meta", "timing", "slowRate"); !ok || got != 0 {
		t.Errorf("QueryFloat = %v, %v", got, ok)
	}
	if got, ok := QueryFloat(data, "jsonNum"); !ok || got != 2.5 {
		t.Errorf("QueryFloat(jsonNum) = %v, %v", got, ok)
	}
	if got, ok := QueryInt(data, "meta", "timing", "plausible"); !ok || got != 7 {
		t.Errorf("QueryInt = %v, %v", got, ok)
	}
	if _, ok := QueryInt(data, "jsonNum"); ok {
		t.Error("QueryInt accepted 2.5")
	}
	if got, ok := QueryBool(data, "flag"); !ok || !got {
		t.Errorf("QueryBool = %v, %v", got, ok)
	}
	if got, ok := QuerySlice(data, "steps"); !ok || len(got) != 2 {
		t.Errorf("QuerySlice = %v, %v", got, ok)
	}
	if got, ok := QueryMap(data, "meta", "timing"); !ok || len(got) != 2 {
		t.Errorf("QueryMap = %v, %v", got, ok)
	}
	if _, ok := QueryMap(data, "steps"); ok {
		t.Error("QueryMap accepted a slice")
	}
}

func TestFloatAndIntConversion(t *testing.T) {
	if got, ok := Float(json.Number("1e3")); !ok || got != 1000 {
		t.Errorf("Float(json.Number) = %v, %v", got, ok)
	}
	if _, ok := Float("1"); ok {
		t.Error("Float accepted a string")
	}
	if got, ok := Int(json.Number("42")); !ok || got != 42 {
		t.Errorf("Int(json.Number) = %v, %v", got, ok)
	}
	if _, ok := Int(uint64(18446744073709551615)); ok {
		t.Error("Int accepted a uint64 overflow")
	}
	if _, ok := Int(1.5); ok {
		t.Error("Int accepted a fraction")
	}
	if got, ok := Int(2.0); !ok || got != 2 {
		t.Errorf("Int(2.0) = %v, %v", got, ok)
	}
	if _, ok := Int(true); ok {
		t.Error("Int accepted a bool")
	}
}

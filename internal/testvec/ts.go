package testvec

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// TSCase mirrors the "ts" group of testdata/vectors.json.
type TSCase struct {
	CRC     CRCCase    `json:"crc32_mpeg2"`
	PIDs    PIDCase    `json:"pids"`
	Streams StreamCase `json:"stream_types"`
	PMTOff  int        `json:"pmt_stream_type_offset"`
	PTSOff  int64      `json:"pts_offset_ticks"`
	ClockHz int64      `json:"clock_hz"`
	Nominal int        `json:"nominal_frame_ms"`
	MaxGap  int        `json:"max_plausible_gap_ms"`
	TableIx int        `json:"table_interval_frames"`

	PTSCases []TickCase `json:"pts_cases"`
	PCRCases []TickCase `json:"pcr_cases"`

	Gap struct {
		DeltaMS int64 `json:"delta_ms"`
		Ticks   int64 `json:"ticks"`
	} `json:"gap_case"`

	Monotonic struct {
		Timestamps []int64 `json:"timestamps_ms"`
		Elapsed    []int64 `json:"expected_elapsed_ms"`
	} `json:"monotonic_case"`
}

type CRCCase struct {
	InputASCII  string `json:"input_ascii"`
	ExpectedHex string `json:"expected_hex"`
}

type PIDCase struct {
	PAT   int `json:"pat"`
	PMT   int `json:"pmt"`
	Video int `json:"video"`
}

type StreamCase struct {
	H264 int `json:"h264"`
	H265 int `json:"h265"`
}

type TickCase struct {
	ID    string `json:"id"`
	Value int64  `json:"value"`
	Hex   string `json:"hex"`
}

// MustTS loads the ts vector group.
func MustTS(tb testing.TB) *TSCase {
	tb.Helper()
	c, err := TS()
	if err != nil {
		tb.Fatalf("testvec: %v", err)
	}
	return c
}

// TS reads and decodes the ts group.
func TS() (*TSCase, error) {
	path, err := find("testdata/vectors.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		TS TSCase `json:"ts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.TS.CRC.ExpectedHex == "" {
		return nil, fmt.Errorf("parse %s: ts group is empty", path)
	}
	return &doc.TS, nil
}

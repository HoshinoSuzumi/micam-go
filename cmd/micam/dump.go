package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"micam/internal/cli"
	"micam/internal/miss"
)

// dumpStream captures the decrypted elementary stream to a file instead of
// serving it.
//
// It exists because "the picture looks wrong" is not a bug report. A raw Annex-B
// dump can be inspected with ffprobe to see the NAL types and picture order,
// which is the only way to tell a bad join point from a decoder problem.
func dumpStream(ctx context.Context, media *miss.Client, opts *cli.Options, log *slog.Logger) (int, bool) {
	file, err := os.Create(opts.Dump)
	if err != nil {
		log.Error("cannot create the dump file", "path", opts.Dump, "err", err)
		return exitSession, false
	}
	defer file.Close()

	out := bufio.NewWriterSize(file, 1<<20)
	defer out.Flush()

	duration := time.Duration(opts.DumpSeconds) * time.Second
	if duration <= 0 {
		duration = 5 * time.Second
	}
	deadline := time.Now().Add(duration)

	var (
		frames     int
		keyframes  int
		bytes      int64
		codec      string
		firstStamp int64
		lastStamp  int64
	)

	log.Info("capturing the elementary stream", "path", opts.Dump, "seconds", opts.DumpSeconds)

	for time.Now().Before(deadline) && ctx.Err() == nil {
		packet, ok, err := media.NextMedia(ctx)
		if err != nil {
			log.Error("media error", "err", err)
			return exitSession, false
		}
		if !ok {
			break
		}
		if !packet.IsVideo() {
			continue
		}

		if _, err := out.Write(packet.Payload); err != nil {
			log.Error("cannot write the dump", "err", err)
			return exitSession, false
		}

		if frames == 0 {
			codec = packet.CodecName()
			firstStamp = packet.TimestampMS
		}
		lastStamp = packet.TimestampMS
		frames++
		bytes += int64(len(packet.Payload))
		if packet.Keyframe() {
			keyframes++
		}
	}

	if err := out.Flush(); err != nil {
		log.Error("cannot flush the dump", "err", err)
		return exitSession, false
	}

	if frames == 0 {
		log.Error("no media arrived, nothing was captured")
		return exitNoMedia, false
	}

	span := float64(lastStamp-firstStamp) / 1000
	log.Info("captured", "frames", frames, "keyframes", keyframes,
		"kb", bytes/1024, "seconds_of_media", fmt.Sprintf("%.1f", span), "codec", codec)

	fmt.Println()
	fmt.Println("  inspect the NAL types with:")
	fmt.Printf("    ffprobe -v error -select_streams v -show_entries frame=pict_type,key_frame -of csv=p=0 %s\n", opts.Dump)
	fmt.Println()

	return exitOK, false
}

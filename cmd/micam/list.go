package main

import (
	"fmt"
	"strings"

	"micam/internal/cloud"
)

// printCameras lists every camera on the account and returns, without touching
// the relay: no UDP socket, no HTTP port, no session. It is the quickest way to
// find the DID that --did wants.
func printCameras(cameras []cloud.Device) int {
	fmt.Println()
	fmt.Printf("  %-12s %-30s %-24s %s\n", "DID", "MODEL", "NAME", "STATUS")
	for _, d := range cameras {
		status := "online"
		if !d.Online {
			status = "OFFLINE"
		}
		fmt.Printf("  %-12s %-30s %-24s %s\n", d.DID, d.Model, truncateName(d.Name, 20), status)
	}
	fmt.Println()
	fmt.Println("  pass one of these with --did")
	fmt.Println()
	return exitOK
}

// truncateName keeps the table aligned when a device has a long name. It counts
// runes, not bytes, so Chinese names are not cut mid-character.
func truncateName(name string, limit int) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

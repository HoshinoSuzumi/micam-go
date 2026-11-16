package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Prompting is only a bootstrap convenience: a container deployment supplies
// everything through flags, environment variables or the credentials file. When
// stdin is not a terminal these readers still work on piped input, which is what
// makes `printf ... | docker run -i` possible.

var stdin = bufio.NewReader(os.Stdin)

// ErrNoInput is returned when stdin is closed before an answer arrives.
var ErrNoInput = errors.New("stdin closed while waiting for an answer")

// Prompt asks for a value, returning fallback on an empty line.
func Prompt(label, fallback string) (string, error) {
	suffix := ": "
	if fallback != "" {
		suffix = " [" + fallback + "]: "
	}
	fmt.Print(label + suffix)

	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", ErrNoInput
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, nil
	}
	return line, nil
}

// PromptRequired repeats until a non-empty value is typed.
func PromptRequired(label string) (string, error) {
	for {
		v, err := Prompt(label, "")
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}
}

// PromptSecret reads a value without echoing only when a terminal is available;
// otherwise it reads a line like any other prompt.
func PromptSecret(label string) (string, error) {
	return Prompt(label, "")
}

// Choose presents a numbered menu and returns the chosen index.
func Choose(title string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, errors.New("nothing to choose from")
	}
	if len(items) == 1 {
		return 0, nil
	}

	fmt.Println()
	fmt.Println(title)
	for i, item := range items {
		fmt.Printf("  %d) %s\n", i+1, item)
	}

	for {
		answer, err := Prompt(fmt.Sprintf("Select [1-%d]", len(items)), "1")
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(answer)
		if err == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
	}
}

// Confirm asks a yes/no question.
func Confirm(label string) (bool, error) {
	answer, err := Prompt(label+" (y/N)", "n")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(answer)
	return answer == "y" || answer == "yes", nil
}

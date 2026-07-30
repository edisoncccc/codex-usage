package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", "", "fixture root")
	gib := flag.Int64("gib", 10, "irrelevant JSON record size in GiB")
	flag.Parse()
	if *root == "" || *gib <= 0 {
		fmt.Fprintln(os.Stderr, "usage: fixturegen -root PATH [-gib 10]")
		os.Exit(2)
	}
	sessionDir := filepath.Join(*root, ".codex", "sessions", "2026", "07", "30")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		panic(err)
	}
	path := filepath.Join(sessionDir, "large-fixture.jsonl")
	file, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	writer := bufio.NewWriterSize(file, 1<<20)
	fmt.Fprintln(writer, `{"timestamp":"2026-07-30T00:00:00Z","type":"session_meta","payload":{"id":"memory-fixture","cwd":"/fixture","originator":"codex_cli_rs"}}`)
	writer.WriteString(`{"timestamp":"2026-07-30T00:00:01Z","type":"response_item","payload":{"content":"`)
	remaining := *gib * (1 << 30)
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for remaining > 0 {
		n := int64(len(chunk))
		if n > remaining {
			n = remaining
		}
		if _, err := writer.Write(chunk[:n]); err != nil {
			panic(err)
		}
		remaining -= n
	}
	writer.WriteString(`"}}` + "\n")
	fmt.Fprintln(writer, `{"timestamp":"2026-07-30T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"cached_input_tokens":20,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":100},"last_token_usage":{"input_tokens":80,"cached_input_tokens":20,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":100}}}}`)
	if err := writer.Flush(); err != nil {
		panic(err)
	}
	if err := file.Close(); err != nil {
		panic(err)
	}
	fmt.Println(path)
}

package main

import (
	"fmt"
	"time"

	"microview/internal/camera"
)

func main() {
	fmt.Println("open: start")
	stream, err := camera.Open()
	if err != nil {
		panic(err)
	}
	fmt.Println("open: success")
	defer stream.Close()

	fmt.Println("debug: start")
	lines, err := stream.DebugPacketHeaders(20, 5*time.Second)
	if err != nil {
		panic(err)
	}
	fmt.Printf("captured %d packet log lines\n", len(lines))
	for _, line := range lines {
		fmt.Println(line)
	}
}

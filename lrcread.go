package main

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func adjustTimeline(scanner *bufio.Scanner) ([]string, error) {
	re := regexp.MustCompile(`(\[\d{2}:\d{2}\.\d{2})\d(\])`)
	timeRe := regexp.MustCompile(`\d{2}:\d{2}\.\d{2}`)

	var updatedContent []string
	var previousTime string

	for scanner.Scan() {
		line := scanner.Text()

		// Remove the last digit from the timeline
		line = re.ReplaceAllString(line, "${1}${2}")

		// Check for duplicate timeline and adjust if necessary
		match := timeRe.FindString(line)
		if match != "" {
			if match == previousTime {
				parts := strings.Split(match, ":")
				minutes, _ := strconv.Atoi(parts[0])
				seconds, _ := strconv.ParseFloat(parts[1], 64)

				totalSeconds := float64(minutes)*60 + seconds + 0.01
				newMinutes := int(totalSeconds / 60)
				newSeconds := totalSeconds - float64(newMinutes)*60

				adjustedTime := fmt.Sprintf("[%02d:%05.2f]", newMinutes, newSeconds)
				line = strings.Replace(line, match, adjustedTime, 1)
			}
			previousTime = match
		}
		updatedContent = append(updatedContent, line)
	}

	return updatedContent, scanner.Err()
}


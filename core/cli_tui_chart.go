//go:build linux && !cgo && cli

package main

import "strings"

const (
	tuiTrafficChartUpload   = "\x1b[38;5;33m"
	tuiTrafficChartDownload = "\x1b[32m"
	tuiTrafficChartOverlap  = "\x1b[36m"
	tuiTrafficChartBaseline = "\x1b[37m"
)

var tuiBrailleDots = [4][2]uint8{
	{1 << 0, 1 << 3},
	{1 << 1, 1 << 4},
	{1 << 2, 1 << 5},
	{1 << 6, 1 << 7},
}

type tuiTrafficChart struct {
	lines []string
	peak  int64
}

func buildTUITrafficChart(
	history []trafficSnapshot,
	width,
	height int,
) tuiTrafficChart {
	if width <= 0 || height <= 0 {
		return tuiTrafficChart{}
	}
	samples := make([]trafficSnapshot, tuiTrafficHistoryLimit)
	if len(history) > tuiTrafficHistoryLimit {
		history = history[len(history)-tuiTrafficHistoryLimit:]
	}
	copy(samples[len(samples)-len(history):], history)

	peak := int64(0)
	for _, sample := range samples {
		peak = maxTUIInt64(peak, maxTUIInt64(sample.Up, sample.Down))
	}
	scale := peak
	if scale <= 0 {
		scale = 1
	}

	up := makeTUIBrailleCanvas(width, height)
	down := makeTUIBrailleCanvas(width, height)
	pixelWidth := width * 2
	pixelHeight := height * 4
	for index := 1; index < len(samples); index++ {
		x0 := (index - 1) * (pixelWidth - 1) / (len(samples) - 1)
		x1 := index * (pixelWidth - 1) / (len(samples) - 1)
		if samples[index-1].Up > 0 || samples[index].Up > 0 {
			drawTUIBrailleLine(
				up,
				x0,
				tuiTrafficChartY(samples[index-1].Up, scale, pixelHeight),
				x1,
				tuiTrafficChartY(samples[index].Up, scale, pixelHeight),
			)
		}
		if samples[index-1].Down > 0 || samples[index].Down > 0 {
			drawTUIBrailleLine(
				down,
				x0,
				tuiTrafficChartY(samples[index-1].Down, scale, pixelHeight),
				x1,
				tuiTrafficChartY(samples[index].Down, scale, pixelHeight),
			)
		}
	}

	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		activeColor := ""
		for column := 0; column < width; column++ {
			upDots := up[row][column]
			downDots := down[row][column]
			color := ""
			dots := upDots | downDots
			switch {
			case upDots != 0 && downDots != 0:
				color = tuiTrafficChartOverlap
			case upDots != 0:
				color = tuiTrafficChartUpload
			case downDots != 0:
				color = tuiTrafficChartDownload
			case row == height-1:
				color = tuiTrafficChartBaseline
			}
			if color != activeColor {
				line.WriteString(tuiReset)
				line.WriteString(color)
				activeColor = color
			}
			if dots == 0 {
				if row == height-1 {
					line.WriteRune('·')
				} else {
					line.WriteByte(' ')
				}
			} else {
				line.WriteRune(rune(0x2800 + int(dots)))
			}
		}
		line.WriteString(tuiReset)
		lines[row] = line.String()
	}
	return tuiTrafficChart{lines: lines, peak: peak}
}

func makeTUIBrailleCanvas(width, height int) [][]uint8 {
	canvas := make([][]uint8, height)
	for row := range canvas {
		canvas[row] = make([]uint8, width)
	}
	return canvas
}

func tuiTrafficChartY(value, scale int64, pixelHeight int) int {
	if value < 0 {
		value = 0
	}
	if value > scale {
		value = scale
	}
	return int((scale - value) * int64(pixelHeight-1) / scale)
}

func drawTUIBrailleLine(canvas [][]uint8, x0, y0, x1, y1 int) {
	deltaX := absTUIChartInt(x1 - x0)
	stepX := -1
	if x0 < x1 {
		stepX = 1
	}
	deltaY := -absTUIChartInt(y1 - y0)
	stepY := -1
	if y0 < y1 {
		stepY = 1
	}
	err := deltaX + deltaY
	for {
		setTUIBrailleDot(canvas, x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		doubled := 2 * err
		if doubled >= deltaY {
			err += deltaY
			x0 += stepX
		}
		if doubled <= deltaX {
			err += deltaX
			y0 += stepY
		}
	}
}

func setTUIBrailleDot(canvas [][]uint8, x, y int) {
	if len(canvas) == 0 || x < 0 || y < 0 {
		return
	}
	row := y / 4
	column := x / 2
	if row >= len(canvas) || column >= len(canvas[row]) {
		return
	}
	canvas[row][column] |= tuiBrailleDots[y%4][x%2]
}

func writeTUIAnsiRow(b *strings.Builder, value string, width int) {
	b.WriteString("│  ")
	b.WriteString(tuiClampAnsiLine(value, maxTUIWidth(width-4, 0)))
	b.WriteString("  │\n")
	b.WriteString(tuiReset)
}

func absTUIChartInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func maxTUIInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

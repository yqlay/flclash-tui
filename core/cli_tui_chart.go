//go:build linux && !cgo && cli

package main

import "strings"

const (
	tuiTrafficChartUpload       = "\x1b[38;5;33m"
	tuiTrafficChartDownload     = "\x1b[38;5;196m"
	tuiTrafficChartOverlap      = "\x1b[38;5;129m"
	tuiTrafficChartUploadFill   = "\x1b[38;5;117m"
	tuiTrafficChartDownloadFill = "\x1b[38;5;217m"
	tuiTrafficChartOverlapFill  = "\x1b[38;5;183m"
	tuiTrafficChartBaseline     = "\x1b[38;5;240m"
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
	plotHeight := height - 1
	lines := make([]string, 0, height)
	if plotHeight <= 0 {
		return tuiTrafficChart{
			lines: []string{
				tuiTrafficChartBaseline + strings.Repeat("─", width) + tuiReset,
			},
		}
	}
	samples := make([]trafficSnapshot, tuiTrafficHistoryLimit)
	if len(history) > tuiTrafficHistoryLimit {
		history = history[len(history)-tuiTrafficHistoryLimit:]
	}
	historyStart := len(samples) - len(history)
	copy(samples[historyStart:], history)

	peak := int64(0)
	for _, sample := range samples {
		peak = maxTUIInt64(peak, maxTUIInt64(sample.Up, sample.Down))
	}
	scale := peak
	if scale <= 0 {
		scale = 1
	}

	up := makeTUIBrailleCanvas(width, plotHeight)
	down := makeTUIBrailleCanvas(width, plotHeight)
	upFill := makeTUIBrailleCanvas(width, plotHeight)
	downFill := makeTUIBrailleCanvas(width, plotHeight)
	pixelWidth := width * 2
	pixelHeight := plotHeight * 4
	drawSeries := func(canvas [][]uint8, value func(trafficSnapshot) int64) {
		if historyStart >= len(samples) {
			return
		}
		if historyStart == len(samples)-1 {
			x := historyStart * (pixelWidth - 1) / (len(samples) - 1)
			setTUIBrailleDot(
				canvas,
				x,
				tuiTrafficChartY(value(samples[historyStart]), scale, pixelHeight),
			)
			return
		}
		for index := historyStart + 1; index < len(samples); index++ {
			x0 := (index - 1) * (pixelWidth - 1) / (len(samples) - 1)
			x1 := index * (pixelWidth - 1) / (len(samples) - 1)
			drawTUIBrailleLine(
				canvas,
				x0,
				tuiTrafficChartY(value(samples[index-1]), scale, pixelHeight),
				x1,
				tuiTrafficChartY(value(samples[index]), scale, pixelHeight),
			)
		}
	}
	drawSeries(up, func(sample trafficSnapshot) int64 { return sample.Up })
	drawSeries(down, func(sample trafficSnapshot) int64 { return sample.Down })
	fillTUIBrailleBelowLine(up, upFill)
	fillTUIBrailleBelowLine(down, downFill)

	for row := 0; row < plotHeight; row++ {
		var line strings.Builder
		activeColor := ""
		for column := 0; column < width; column++ {
			upDots := up[row][column]
			downDots := down[row][column]
			upFillDots := upFill[row][column]
			downFillDots := downFill[row][column]
			color := ""
			dots := upDots | downDots | upFillDots | downFillDots
			switch {
			case upDots != 0 && downDots != 0:
				color = tuiTrafficChartOverlap
			case upDots != 0:
				color = tuiTrafficChartUpload
			case downDots != 0:
				color = tuiTrafficChartDownload
			case upFillDots != 0 && downFillDots != 0:
				color = tuiTrafficChartOverlapFill
			case upFillDots != 0:
				color = tuiTrafficChartUploadFill
			case downFillDots != 0:
				color = tuiTrafficChartDownloadFill
			}
			if color != activeColor {
				line.WriteString(tuiReset)
				line.WriteString(color)
				activeColor = color
			}
			if dots == 0 {
				line.WriteByte(' ')
			} else {
				line.WriteRune(rune(0x2800 + int(dots)))
			}
		}
		line.WriteString(tuiReset)
		lines = append(lines, line.String())
	}
	lines = append(
		lines,
		tuiTrafficChartBaseline+strings.Repeat("─", width)+tuiReset,
	)
	return tuiTrafficChart{lines: lines, peak: peak}
}

func fillTUIBrailleBelowLine(line, fill [][]uint8) {
	if len(line) == 0 || len(fill) != len(line) {
		return
	}
	pixelWidth := len(line[0]) * 2
	pixelHeight := len(line) * 4
	for x := 0; x < pixelWidth; x++ {
		lineY := -1
		for y := 0; y < pixelHeight; y++ {
			if hasTUIBrailleDot(line, x, y) {
				lineY = y
				break
			}
		}
		if lineY < 0 {
			continue
		}
		for y := lineY + 1; y < pixelHeight; y++ {
			setTUIBrailleDot(fill, x, y)
		}
	}
}

func hasTUIBrailleDot(canvas [][]uint8, x, y int) bool {
	if len(canvas) == 0 || x < 0 || y < 0 {
		return false
	}
	row := y / 4
	column := x / 2
	if row >= len(canvas) || column >= len(canvas[row]) {
		return false
	}
	return canvas[row][column]&tuiBrailleDots[y%4][x%2] != 0
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

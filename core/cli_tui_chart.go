//go:build linux && !cgo && cli

package main

import (
	"math"
	"strings"
)

// The live chart follows the Flutter dashboard painter: one quadratic-Bezier
// curve of upload+download, a two-pixel stroke, and a fill that fades downward.
const (
	tuiTrafficChartColor = tuiCyan
	tuiTrafficChartFill  = tuiDim + tuiCyan
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

type tuiChartPoint struct {
	x int
	y int
}

func buildTUITrafficChart(
	history []trafficSnapshot,
	width,
	height int,
) tuiTrafficChart {
	if width <= 0 || height <= 0 {
		return tuiTrafficChart{}
	}
	if len(history) > tuiTrafficHistoryLimit {
		history = history[len(history)-tuiTrafficHistoryLimit:]
	}
	// Two leading zeros match the Flutter chart's initPoints, so an empty
	// history is a flat line and the first real sample leaves the baseline.
	values := make([]int64, 0, len(history)+2)
	values = append(values, 0, 0)
	var peak int64
	for _, sample := range history {
		speed := tuiTrafficSpeed(sample)
		values = append(values, speed)
		peak = maxTUIInt64(peak, speed)
	}

	pixelWidth := width * 2
	pixelHeight := height * 4
	baseline := int(math.Round(0.70 * float64(pixelHeight-1)))
	points := make([]tuiChartPoint, len(values))
	for index, value := range values {
		points[index] = tuiChartPoint{
			x: index * (pixelWidth - 1) / (len(values) - 1),
			y: tuiTrafficChartY(value, peak, baseline),
		}
	}

	curveY := make([]int, pixelWidth)
	for index := range curveY {
		curveY[index] = baseline
	}
	plot := func(from, to tuiChartPoint) {
		coverTUIChartSegment(curveY, pixelWidth, pixelHeight, from, to)
	}
	if len(points) < 3 {
		plot(points[0], points[len(points)-1])
	} else {
		current := points[0]
		for index := 1; index < len(points)-1; index++ {
			end := tuiChartPoint{
				x: int(math.Round(float64(points[index].x+points[index+1].x) / 2)),
				y: int(math.Round(float64(points[index].y+points[index+1].y) / 2)),
			}
			previous := current
			for _, point := range tuiQuadraticPoints(current, points[index], end) {
				plot(previous, point)
				previous = point
			}
			current = end
		}
		plot(current, points[len(points)-1])
	}

	stroke := makeTUIBrailleCanvas(width, height)
	fill := makeTUIBrailleCanvas(width, height)
	for x, y := range curveY {
		setTUIBrailleDot(stroke, x, y)
		setTUIBrailleDot(stroke, x, y+1)
		span := pixelHeight - 1 - y
		if span < 1 {
			span = 1
		}
		for py := y; py < pixelHeight; py++ {
			if keepTUITrafficFillDot(x, py, py-y, span) {
				setTUIBrailleDot(fill, x, py)
			}
		}
	}

	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		activeColor := ""
		for column := 0; column < width; column++ {
			dots := fill[row][column]
			color := ""
			if dots != 0 {
				color = tuiTrafficChartFill
			}
			if stroke[row][column] != 0 {
				dots |= stroke[row][column]
				color = tuiTrafficChartColor
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
		lines[row] = line.String()
	}
	return tuiTrafficChart{lines: lines, peak: peak}
}

func tuiTrafficSpeed(sample trafficSnapshot) int64 {
	up := sample.Up
	down := sample.Down
	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}
	if up > math.MaxInt64-down {
		return math.MaxInt64
	}
	return up + down
}

func tuiTrafficChartY(value, peak int64, baseline int) int {
	if value <= 0 || peak <= 0 {
		return baseline
	}
	if value > peak {
		value = peak
	}
	return baseline - int(value*int64(baseline)/peak)
}

// tuiQuadraticPoints samples B(t)=(1-t)²P0+2(1-t)tP1+t²P2. P1 is the control
// point, so the curve bends toward a sample and ends on the next midpoint.
func tuiQuadraticPoints(start, control, end tuiChartPoint) []tuiChartPoint {
	steps := absTUIChartInt(end.x-start.x) + absTUIChartInt(end.y-start.y)
	if steps < 8 {
		steps = 8
	}
	points := make([]tuiChartPoint, 0, steps)
	for step := 1; step <= steps; step++ {
		t := float64(step) / float64(steps)
		u := 1 - t
		points = append(points, tuiChartPoint{
			x: int(math.Round(u*u*float64(start.x) + 2*u*t*float64(control.x) + t*t*float64(end.x))),
			y: int(math.Round(u*u*float64(start.y) + 2*u*t*float64(control.y) + t*t*float64(end.y))),
		})
	}
	return points
}

func coverTUIChartSegment(curveY []int, pixelWidth, pixelHeight int, from, to tuiChartPoint) {
	x0, y0 := from.x, from.y
	x1, y1 := to.x, to.y
	if x1 < x0 {
		x0, x1 = x1, x0
		y0, y1 = y1, y0
	}
	if x1 < 0 || x0 >= pixelWidth {
		return
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= pixelWidth {
		x1 = pixelWidth - 1
	}
	record := func(x, y int) {
		if x < 0 || x >= len(curveY) {
			return
		}
		if y < 0 {
			y = 0
		}
		if y >= pixelHeight {
			y = pixelHeight - 1
		}
		if y < curveY[x] {
			curveY[x] = y
		}
	}
	if x0 == x1 {
		record(x0, y0)
		record(x0, y1)
		return
	}
	for x := x0; x <= x1; x++ {
		y := y0 + (y1-y0)*(x-x0)/(x1-x0)
		record(x, y)
	}
}

// keepTUITrafficFillDot fades the fill from about 38% density at the curve
// to about 10% at the bottom, the terminal stand-in for the Flutter gradient.
func keepTUITrafficFillDot(x, y, depth, span int) bool {
	if span < 1 {
		span = 1
	}
	percent := 38 - 28*depth/span
	if percent < 10 {
		percent = 10
	}
	bucket := (x*73 + y*37) % 100
	if bucket < 0 {
		bucket += 100
	}
	return bucket < percent
}

func makeTUIBrailleCanvas(width, height int) [][]uint8 {
	canvas := make([][]uint8, height)
	for row := range canvas {
		canvas[row] = make([]uint8, width)
	}
	return canvas
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

//go:build linux && !cgo && cli

package main

import (
	"math"
	"strings"
)

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
	// Same fit as the original line chart: each interior sample is a quadratic
	// control, and the curve ends at the midpoint toward the next sample.
	drawTUIFittedSeries(up, samples, scale, pixelWidth, pixelHeight, true)
	drawTUIFittedSeries(down, samples, scale, pixelWidth, pixelHeight, false)
	// A braille cell is two dots wide. A steep run lights both columns across
	// several rows and reads as a filled stroke. Pinch that back to one dot
	// wide, and keep every row the run actually crossed so the line stays joined.
	thinTUIBrailleStroke(up)
	thinTUIBrailleStroke(down)

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

type tuiChartPoint struct {
	x int
	y int
}

func drawTUIFittedSeries(
	canvas [][]uint8,
	samples []trafficSnapshot,
	scale int64,
	pixelWidth int,
	pixelHeight int,
	upload bool,
) {
	count := len(samples)
	if count < 2 || pixelWidth <= 1 || pixelHeight <= 1 {
		return
	}
	points := make([]tuiChartPoint, count)
	values := make([]int64, count)
	for index, sample := range samples {
		value := sample.Down
		if upload {
			value = sample.Up
		}
		if value < 0 {
			value = 0
		}
		values[index] = value
		points[index] = tuiChartPoint{
			x: tuiTrafficChartX(index, count, pixelWidth),
			y: tuiTrafficChartY(value, scale, pixelHeight),
		}
	}
	if count == 2 {
		if values[0] > 0 || values[1] > 0 {
			drawTUIBrailleLine(canvas, points[0].x, points[0].y, points[1].x, points[1].y)
		}
		return
	}
	current := points[0]
	for index := 1; index < count-1; index++ {
		end := tuiChartMidpoint(points[index], points[index+1])
		if tuiFittedSpanLive(values, index) {
			drawTUIQuadraticCurve(canvas, current, points[index], end, pixelHeight)
		}
		current = end
	}
	if values[count-2] > 0 || values[count-1] > 0 {
		drawTUIBrailleLine(
			canvas,
			current.x,
			current.y,
			points[count-1].x,
			points[count-1].y,
		)
	}
}

func tuiTrafficChartX(index, count, pixelWidth int) int {
	if count <= 1 || pixelWidth <= 1 {
		return 0
	}
	return index * (pixelWidth - 1) / (count - 1)
}

func tuiChartMidpoint(left, right tuiChartPoint) tuiChartPoint {
	return tuiChartPoint{
		x: roundTUIChart(float64(left.x+right.x) / 2),
		y: roundTUIChart(float64(left.y+right.y) / 2),
	}
}

func tuiFittedSpanLive(values []int64, index int) bool {
	if index < 0 || index+1 >= len(values) {
		return false
	}
	if values[index] > 0 || values[index+1] > 0 {
		return true
	}
	return index > 0 && values[index-1] > 0
}

func drawTUIQuadraticCurve(
	canvas [][]uint8,
	start tuiChartPoint,
	control tuiChartPoint,
	end tuiChartPoint,
	pixelHeight int,
) {
	if start.x == end.x {
		drawTUIBrailleLine(canvas, start.x, start.y, end.x, end.y)
		return
	}
	step := 1
	if end.x < start.x {
		step = -1
	}
	previousX := start.x
	previousY := start.y
	for x := start.x + step; x != end.x+step; x += step {
		y := end.y
		if x != end.x {
			t := tuiQuadraticTAtX(float64(start.x), float64(control.x), float64(end.x), float64(x))
			y = clampTUIChartY(
				roundTUIChart(tuiQuadraticBezier(float64(start.y), float64(control.y), float64(end.y), t)),
				pixelHeight,
			)
		}
		drawTUIBrailleLine(canvas, previousX, previousY, x, y)
		previousX = x
		previousY = y
	}
}

func tuiQuadraticBezier(p0, p1, p2, t float64) float64 {
	u := 1 - t
	return u*u*p0 + 2*u*t*p1 + t*t*p2
}

func tuiQuadraticTAtX(x0, controlX, x2, target float64) float64 {
	a := x0 - 2*controlX + x2
	b := 2 * (controlX - x0)
	c := x0 - target
	if math.Abs(a) < 1e-9 {
		if math.Abs(x2-x0) < 1e-9 {
			return 0
		}
		return clampTUIUnit((target - x0) / (x2 - x0))
	}
	discriminant := b*b - 4*a*c
	if discriminant < 0 {
		discriminant = 0
	}
	root := math.Sqrt(discriminant)
	plus := (-b + root) / (2 * a)
	minus := (-b - root) / (2 * a)
	if plus >= -1e-6 && plus <= 1+1e-6 {
		return clampTUIUnit(plus)
	}
	return clampTUIUnit(minus)
}

func clampTUIUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func roundTUIChart(value float64) int {
	if value >= 0 {
		return int(value + 0.5)
	}
	return int(value - 0.5)
}

func clampTUIChartY(y, pixelHeight int) int {
	if y < 0 {
		return 0
	}
	if pixelHeight <= 0 || y > pixelHeight-1 {
		return max(pixelHeight-1, 0)
	}
	return y
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

func thinTUIBrailleStroke(canvas [][]uint8) {
	if len(canvas) == 0 {
		return
	}
	original := make([][]uint8, len(canvas))
	for row := range canvas {
		original[row] = append([]uint8(nil), canvas[row]...)
	}
	for row := range original {
		for column, dots := range original[row] {
			canvas[row][column] = thinTUIBrailleCell(
				original,
				row,
				column,
				dots,
				tuiBrailleHasVerticalNeighbor(original, row, column),
			)
		}
	}
}

func tuiBrailleHasVerticalNeighbor(canvas [][]uint8, row, column int) bool {
	if row > 0 && canvas[row-1][column] != 0 {
		return true
	}
	return row+1 < len(canvas) && canvas[row+1][column] != 0
}

func thinTUIBrailleCell(
	canvas [][]uint8,
	row int,
	column int,
	dots uint8,
	vertical bool,
) uint8 {
	if dots == 0 {
		return 0
	}
	occupiedRows := 0
	leftCount := 0
	rightCount := 0
	doubleRows := 0
	for dotRow := 0; dotRow < 4; dotRow++ {
		left := dots&tuiBrailleDots[dotRow][0] != 0
		right := dots&tuiBrailleDots[dotRow][1] != 0
		if !left && !right {
			continue
		}
		occupiedRows++
		if left {
			leftCount++
		}
		if right {
			rightCount++
		}
		if left && right {
			doubleRows++
		}
	}
	// One row may use both columns: that is the horizontal hairline (⠉⠒⠤⣀).
	// A diagonal, or one full row plus a single-dot neighbor, is still one
	// stroke. Dropping that neighbor opens a hole between pixel rows.
	// Pinch only a cell that fills both columns on two or more rows, and
	// keep every row the run crossed.
	if occupiedRows <= 1 || leftCount == 0 || rightCount == 0 || doubleRows == 0 {
		return dots
	}
	if doubleRows < 2 && !vertical && occupiedRows < 3 {
		return dots
	}
	leftRows, leftTouches := tuiBrailleSideTouchRows(canvas, row, column, 0)
	rightRows, rightTouches := tuiBrailleSideTouchRows(canvas, row, column, 1)
	if leftTouches && rightTouches {
		return bridgeTUIBrailleCell(dots, leftRows, rightRows)
	}
	if rightTouches {
		return collapseTUIBrailleToColumn(dots, true)
	}
	if leftTouches {
		return collapseTUIBrailleToColumn(dots, false)
	}
	return collapseTUIBrailleToColumn(dots, rightCount > leftCount)
}

func tuiBrailleSideTouchRows(
	canvas [][]uint8,
	row int,
	column int,
	side int,
) (rows [4]bool, touches bool) {
	dots := canvas[row][column]
	x := column*2 + side
	for dotRow := 0; dotRow < 4; dotRow++ {
		if dots&tuiBrailleDots[dotRow][side] == 0 {
			continue
		}
		if !tuiBraillePixelTouchesOutside(canvas, x, row*4+dotRow, row, column) {
			continue
		}
		rows[dotRow] = true
		touches = true
	}
	return rows, touches
}

func tuiBraillePixelTouchesOutside(
	canvas [][]uint8,
	x int,
	y int,
	row int,
	column int,
) bool {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := x + dx
			ny := y + dy
			if nx < 0 || ny < 0 {
				continue
			}
			nRow := ny / 4
			nColumn := nx / 2
			if nRow == row && nColumn == column {
				continue
			}
			if tuiCanvasHasPixel(canvas, nx, ny) {
				return true
			}
		}
	}
	return false
}

func tuiCanvasHasPixel(canvas [][]uint8, x, y int) bool {
	if x < 0 || y < 0 || len(canvas) == 0 {
		return false
	}
	row := y / 4
	column := x / 2
	if row >= len(canvas) || column >= len(canvas[row]) {
		return false
	}
	return canvas[row][column]&tuiBrailleDots[y%4][x%2] != 0
}

func bridgeTUIBrailleCell(dots uint8, leftRows, rightRows [4]bool) uint8 {
	bridgeRow := -1
	for dotRow := 3; dotRow >= 0; dotRow-- {
		left := dots&tuiBrailleDots[dotRow][0] != 0
		right := dots&tuiBrailleDots[dotRow][1] != 0
		if left && right && rightRows[dotRow] {
			bridgeRow = dotRow
			break
		}
	}
	if bridgeRow < 0 {
		for dotRow := 3; dotRow >= 0; dotRow-- {
			left := dots&tuiBrailleDots[dotRow][0] != 0
			right := dots&tuiBrailleDots[dotRow][1] != 0
			if left && right && (leftRows[dotRow] || rightRows[dotRow]) {
				bridgeRow = dotRow
				break
			}
		}
	}
	if bridgeRow < 0 {
		for dotRow := 0; dotRow < 4; dotRow++ {
			left := dots&tuiBrailleDots[dotRow][0] != 0
			right := dots&tuiBrailleDots[dotRow][1] != 0
			if left && right {
				bridgeRow = dotRow
				break
			}
		}
	}
	var out uint8
	for dotRow := 0; dotRow < 4; dotRow++ {
		left := dots&tuiBrailleDots[dotRow][0] != 0
		right := dots&tuiBrailleDots[dotRow][1] != 0
		if !left && !right {
			continue
		}
		if dotRow == bridgeRow {
			if left {
				out |= tuiBrailleDots[dotRow][0]
			}
			if right {
				out |= tuiBrailleDots[dotRow][1]
			}
			continue
		}
		if left && right {
			if dotRow < bridgeRow {
				out |= tuiBrailleDots[dotRow][0]
			} else {
				out |= tuiBrailleDots[dotRow][1]
			}
			continue
		}
		if left {
			out |= tuiBrailleDots[dotRow][0]
		} else {
			out |= tuiBrailleDots[dotRow][1]
		}
	}
	return out
}

func collapseTUIBrailleToColumn(dots uint8, keepRight bool) uint8 {
	side := 0
	if keepRight {
		side = 1
	}
	var out uint8
	for dotRow := 0; dotRow < 4; dotRow++ {
		if dots&(tuiBrailleDots[dotRow][0]|tuiBrailleDots[dotRow][1]) == 0 {
			continue
		}
		out |= tuiBrailleDots[dotRow][side]
	}
	return out
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

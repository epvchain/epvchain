
package termui

import "fmt"

type BarChart struct {
	Block
	BarColor   Attribute
	TextColor  Attribute
	NumColor   Attribute
	Data       []int
	DataLabels []string
	BarWidth   int
	BarGap     int
	CellChar   rune
	labels     [][]rune
	dataNum    [][]rune
	numBar     int
	scale      float64
	max        int
}

func NewBarChart() *BarChart {
	bc := &BarChart{Block: *NewBlock()}
	bc.BarColor = ThemeAttr("barchart.bar.bg")
	bc.NumColor = ThemeAttr("barchart.num.fg")
	bc.TextColor = ThemeAttr("barchart.text.fg")
	bc.BarGap = 1
	bc.BarWidth = 3
	bc.CellChar = ' '
	return bc
}

func (bc *BarChart) layout() {
	bc.numBar = bc.innerArea.Dx() / (bc.BarGap + bc.BarWidth)
	bc.labels = make([][]rune, bc.numBar)
	bc.dataNum = make([][]rune, len(bc.Data))

	for i := 0; i < bc.numBar && i < len(bc.DataLabels) && i < len(bc.Data); i++ {
		bc.labels[i] = trimStr2Runes(bc.DataLabels[i], bc.BarWidth)
		n := bc.Data[i]
		s := fmt.Sprint(n)
		bc.dataNum[i] = trimStr2Runes(s, bc.BarWidth)
	}

	if bc.max == 0 {
		bc.max = -1
	}
	for i := 0; i < len(bc.Data); i++ {
		if bc.max < bc.Data[i] {
			bc.max = bc.Data[i]
		}
	}
	bc.scale = float64(bc.max) / float64(bc.innerArea.Dy()-1)
}

func (bc *BarChart) SetMax(max int) {

	if max > 0 {
		bc.max = max
	}
}

func (bc *BarChart) Buffer() Buffer {
	buf := bc.Block.Buffer()
	bc.layout()

	for i := 0; i < bc.numBar && i < len(bc.Data) && i < len(bc.DataLabels); i++ {
		h := int(float64(bc.Data[i]) / bc.scale)
		oftX := i * (bc.BarWidth + bc.BarGap)

		barBg := bc.Bg
		barFg := bc.BarColor

		if bc.CellChar == ' ' {
			barBg = bc.BarColor
			barFg = ColorDefault
			if bc.BarColor == ColorDefault { 
				barBg |= AttrReverse
			}
		}

		for j := 0; j < bc.BarWidth; j++ {
			for k := 0; k < h; k++ {
				c := Cell{
					Ch: bc.CellChar,
					Bg: barBg,
					Fg: barFg,
				}

				x := bc.innerArea.Min.X + i*(bc.BarWidth+bc.BarGap) + j
				y := bc.innerArea.Min.Y + bc.innerArea.Dy() - 2 - k
				buf.Set(x, y, c)
			}
		}

		for j, k := 0, 0; j < len(bc.labels[i]); j++ {
			w := charWidth(bc.labels[i][j])
			c := Cell{
				Ch: bc.labels[i][j],
				Bg: bc.Bg,
				Fg: bc.TextColor,
			}
			y := bc.innerArea.Min.Y + bc.innerArea.Dy() - 1
			x := bc.innerArea.Min.X + oftX + k
			buf.Set(x, y, c)
			k += w
		}

		for j := 0; j < len(bc.dataNum[i]); j++ {
			c := Cell{
				Ch: bc.dataNum[i][j],
				Fg: bc.NumColor,
				Bg: barBg,
			}

			if h == 0 {
				c.Bg = bc.Bg
			}
			x := bc.innerArea.Min.X + oftX + (bc.BarWidth-len(bc.dataNum[i]))/2 + j
			y := bc.innerArea.Min.Y + bc.innerArea.Dy() - 2
			buf.Set(x, y, c)
		}
	}

	return buf
}

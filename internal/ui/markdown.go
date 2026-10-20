package ui

import (
	"fmt"
	"image/color"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
)

// Markdown renders a Markdown document for the terminal in the brand
// colors, wrapped at width.
func (t Theme) Markdown(md string, width int) (string, error) {
	r, err := glamour.NewTermRenderer(glamour.WithStyles(t.markdownStyle()), glamour.WithWordWrap(width))
	if err != nil {
		return "", err
	}
	return r.Render(md)
}

func hexOf(c color.Color) *string {
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}

func (t Theme) markdownStyle() ansi.StyleConfig {
	st := styles.LightStyleConfig
	if t.Dark {
		st = styles.DarkStyleConfig
	}
	p := t.P
	yes := true
	st.Document.Color = hexOf(p.Text)
	st.Heading.Color = hexOf(p.Text)
	st.H1.Color = hexOf(p.Ink)
	st.H1.BackgroundColor = hexOf(p.Ore)
	st.H1.Bold = &yes
	st.H2.Color = hexOf(p.Ore)
	st.H2.Prefix = "▌ "
	st.H3.Color = hexOf(p.Text)
	st.H3.Prefix = "◆ "
	st.Strong.Color = hexOf(p.Text)
	st.Link.Color = hexOf(p.Ore)
	st.LinkText.Color = hexOf(p.Ore)
	st.Code.Color = hexOf(p.Gem)
	st.Code.BackgroundColor = hexOf(p.Surface)
	st.BlockQuote.Color = hexOf(p.Text)
	bar := "┃ "
	st.BlockQuote.IndentToken = &bar
	st.HorizontalRule.Color = hexOf(p.Faint)
	st.Item.Color = hexOf(p.Text)
	st.Table.Color = hexOf(p.Text)
	if st.CodeBlock.Chroma != nil {
		ch := *st.CodeBlock.Chroma
		ch.GenericInserted.Color = hexOf(p.Gem)
		ch.GenericDeleted.Color = hexOf(p.Ember)
		ch.GenericSubheading.Color = hexOf(p.Violet)
		st.CodeBlock.Chroma = &ch
	}
	return st
}

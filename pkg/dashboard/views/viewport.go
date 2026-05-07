package views

// Viewport is the small subset of viewport behavior needed by the headless
// dashboard renderer.
type Viewport struct {
	width   int
	height  int
	content string
}

func NewViewport(width, height int) Viewport {
	return Viewport{width: width, height: height}
}

func (v *Viewport) SetContent(content string) {
	v.content = content
}

func (v *Viewport) GotoTop() {}

func (v *Viewport) SetWidth(width int) {
	v.width = width
}

func (v *Viewport) SetHeight(height int) {
	v.height = height
}

func (v Viewport) Width() int {
	return v.width
}

func (v Viewport) Height() int {
	return v.height
}

func (v Viewport) View() string {
	return v.content
}

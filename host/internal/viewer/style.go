package viewer

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Status dot classes, in GNOME's status colours (docs/design.md, Colour). A
// dot without one is grey, as for a computer that is asleep or unknown.
const (
	dotOnline      = "online"
	dotWaking      = "waking"
	dotAsleep      = "asleep"
	dotUnreachable = "unreachable"
)

// appCSS styles what libadwaita has no class for: the status dots.
const appCSS = `
.status-dot { min-width: 10px; min-height: 10px; border-radius: 5px; background-color: #9A9996; }
.status-dot.online { background-color: #26A269; }
.status-dot.waking { background-color: #E5A50A; }
.status-dot.unreachable { background-color: #E01B24; }
`

// darkCSS holds appCSS's lighter colours for dark mode.
const darkCSS = `
.status-dot { background-color: #77767B; }
.status-dot.online { background-color: #33D17A; }
.status-dot.waking { background-color: #F6D32D; }
.status-dot.unreachable { background-color: #F66151; }
`

// loadStyle adds appCSS to the display, above libadwaita's stylesheet, and
// darkCSS above it while libadwaita is dark. A prefers-color-scheme media
// query would not do: libadwaita tells only its own stylesheets when the
// app turns dark.
func loadStyle(display *gdk.Display) {
	css := gtk.NewCSSProvider()
	css.LoadFromString(appCSS)
	gtk.StyleContextAddProviderForDisplay(display, css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	dark := gtk.NewCSSProvider()
	dark.LoadFromString(darkCSS)
	styles := adw.StyleManagerGetDefault()
	added := false
	follow := func() {
		switch {
		case styles.Dark() && !added:
			gtk.StyleContextAddProviderForDisplay(display, dark, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION+1)
		case !styles.Dark() && added:
			gtk.StyleContextRemoveProviderForDisplay(display, dark)
		}
		added = styles.Dark()
	}
	follow()
	styles.NotifyProperty("dark", follow)
}

// newStatusDot is a round dot that shows a status beside the words that
// say it, so colour never carries the meaning alone.
func newStatusDot() *gtk.Box {
	dot := gtk.NewBox(gtk.OrientationHorizontal, 0)
	dot.SetVAlign(gtk.AlignCenter)
	setDot(dot, "")
	return dot
}

// setDot gives dot the colour of class, one of the dot classes or "" for
// grey.
func setDot(dot *gtk.Box, class string) {
	classes := []string{"status-dot"}
	if class != "" {
		classes = append(classes, class)
	}
	dot.SetCSSClasses(classes)
}

// slidersSVG is a sliders icon in GNOME's symbolic style; Adwaita's icon
// theme has none. url(#gpa:foreground) makes GtkSvg paint it in the text
// colour, as GTK paints symbolic icons.
const slidersSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
<g fill="none" stroke="url(#gpa:foreground) #241f31" stroke-width="1.5">
<path d="M1 4h6.5M13.5 4H15M1 12h1.5M8.5 12H15"/>
<circle cx="10.5" cy="4" r="2.25"/>
<circle cx="5.5" cy="12" r="2.25"/>
</g>
</svg>`

// slidersIcon is the sliders icon at the size of a symbolic icon.
func slidersIcon() *gtk.Image {
	img := gtk.NewImageFromPaintable(gtk.NewSVGFromBytes(glibv2.NewBytes([]byte(slidersSVG))))
	img.SetPixelSize(16)
	return img
}

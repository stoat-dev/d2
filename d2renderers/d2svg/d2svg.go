// d2svg implements an SVG renderer for d2 diagrams.
// The input is d2exporter's output
package d2svg

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"sort"
	"strings"

	"math"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"oss.terrastruct.com/util-go/go2"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2renderers/d2fonts"
	"oss.terrastruct.com/d2/d2renderers/d2latex"
	"oss.terrastruct.com/d2/d2renderers/d2sketch"
	"oss.terrastruct.com/d2/d2target"
	"oss.terrastruct.com/d2/d2themes"
	"oss.terrastruct.com/d2/d2themes/d2themescatalog"
	"oss.terrastruct.com/d2/lib/color"
	"oss.terrastruct.com/d2/lib/geo"
	"oss.terrastruct.com/d2/lib/label"
	"oss.terrastruct.com/d2/lib/shape"
	"oss.terrastruct.com/d2/lib/svg"
	"oss.terrastruct.com/d2/lib/textmeasure"
)

const (
	DEFAULT_PADDING            = 100
	MIN_ARROWHEAD_STROKE_WIDTH = 2

	appendixIconRadius = 16
)

var multipleOffset = geo.NewVector(d2target.MULTIPLE_OFFSET, -d2target.MULTIPLE_OFFSET)

//go:embed tooltip.svg
var TooltipIcon string

//go:embed link.svg
var LinkIcon string

//go:embed style.css
var baseStylesheet string

//go:embed github-markdown.css
var mdCSS string

type RenderOpts struct {
	Pad         int
	Sketch      bool
	ThemeID     int64
	DarkThemeID *int64
	// disables the fit to screen behavior and ensures the exported svg has the exact dimensions
	SetDimensions bool
}

func dimensions(diagram *d2target.Diagram, pad int) (left, top, width, height int) {
	tl, br := diagram.BoundingBox()
	left = tl.X - pad
	top = tl.Y - pad
	width = br.X - tl.X + pad*2
	height = br.Y - tl.Y + pad*2

	return left, top, width, height
}

func arrowheadMarkerID(isTarget bool, connection d2target.Connection) string {
	var arrowhead d2target.Arrowhead
	if isTarget {
		arrowhead = connection.DstArrow
	} else {
		arrowhead = connection.SrcArrow
	}

	return fmt.Sprintf("mk-%s", hash(fmt.Sprintf("%s,%t,%d,%s",
		arrowhead, isTarget, connection.StrokeWidth, connection.Stroke,
	)))
}

func arrowheadDimensions(arrowhead d2target.Arrowhead, strokeWidth float64) (width, height float64) {
	var widthMultiplier float64
	var heightMultiplier float64
	switch arrowhead {
	case d2target.ArrowArrowhead:
		widthMultiplier = 5
		heightMultiplier = 5
	case d2target.TriangleArrowhead:
		widthMultiplier = 5
		heightMultiplier = 6
	case d2target.LineArrowhead:
		widthMultiplier = 5
		heightMultiplier = 8
	case d2target.FilledDiamondArrowhead:
		widthMultiplier = 11
		heightMultiplier = 7
	case d2target.DiamondArrowhead:
		widthMultiplier = 11
		heightMultiplier = 9
	case d2target.FilledCircleArrowhead, d2target.CircleArrowhead:
		widthMultiplier = 12
		heightMultiplier = 12
	case d2target.CfOne, d2target.CfMany, d2target.CfOneRequired, d2target.CfManyRequired:
		widthMultiplier = 9
		heightMultiplier = 9
	}

	clippedStrokeWidth := go2.Max(MIN_ARROWHEAD_STROKE_WIDTH, strokeWidth)
	return clippedStrokeWidth * widthMultiplier, clippedStrokeWidth * heightMultiplier
}

func arrowheadMarker(isTarget bool, id string, connection d2target.Connection) string {
	arrowhead := connection.DstArrow
	if !isTarget {
		arrowhead = connection.SrcArrow
	}
	strokeWidth := float64(connection.StrokeWidth)
	width, height := arrowheadDimensions(arrowhead, strokeWidth)

	var path string
	switch arrowhead {
	case d2target.ArrowArrowhead:
		polygonEl := d2themes.NewThemableElement("polygon")
		polygonEl.Fill = connection.Stroke
		polygonEl.ClassName = "connection"
		polygonEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				0., 0.,
				width, height/2,
				0., height,
				width/4, height/2,
			)
		} else {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				0., height/2,
				width, 0.,
				width*3/4, height/2,
				width, height,
			)
		}
		path = polygonEl.Render()
	case d2target.TriangleArrowhead:
		polygonEl := d2themes.NewThemableElement("polygon")
		polygonEl.Fill = connection.Stroke
		polygonEl.ClassName = "connection"
		polygonEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f",
				0., 0.,
				width, height/2.0,
				0., height,
			)
		} else {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f",
				width, 0.,
				0., height/2.0,
				width, height,
			)
		}
		path = polygonEl.Render()
	case d2target.LineArrowhead:
		polylineEl := d2themes.NewThemableElement("polyline")
		polylineEl.Fill = color.None
		polylineEl.ClassName = "connection"
		polylineEl.Stroke = connection.Stroke
		polylineEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			polylineEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f",
				strokeWidth/2, strokeWidth/2,
				width-strokeWidth/2, height/2,
				strokeWidth/2, height-strokeWidth/2,
			)
		} else {
			polylineEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f",
				width-strokeWidth/2, strokeWidth/2,
				strokeWidth/2, height/2,
				width-strokeWidth/2, height-strokeWidth/2,
			)
		}
		path = polylineEl.Render()
	case d2target.FilledDiamondArrowhead:
		polygonEl := d2themes.NewThemableElement("polygon")
		polygonEl.ClassName = "connection"
		polygonEl.Fill = connection.Stroke
		polygonEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				0., height/2.0,
				width/2.0, 0.,
				width, height/2.0,
				width/2.0, height,
			)
		} else {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				0., height/2.0,
				width/2.0, 0.,
				width, height/2.0,
				width/2.0, height,
			)
		}
		path = polygonEl.Render()
	case d2target.DiamondArrowhead:
		polygonEl := d2themes.NewThemableElement("polygon")
		polygonEl.ClassName = "connection"
		polygonEl.Fill = d2target.BG_COLOR
		polygonEl.Stroke = connection.Stroke
		polygonEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				0., height/2.0,
				width/2, height/8,
				width, height/2.0,
				width/2.0, height*0.9,
			)
		} else {
			polygonEl.Points = fmt.Sprintf("%f,%f %f,%f %f,%f %f,%f",
				width/8, height/2.0,
				width*0.6, height/8,
				width*1.1, height/2.0,
				width*0.6, height*7/8,
			)
		}
		path = polygonEl.Render()
	case d2target.FilledCircleArrowhead:
		radius := width / 2

		circleEl := d2themes.NewThemableElement("circle")
		circleEl.Cy = radius
		circleEl.R = radius - strokeWidth/2
		circleEl.Fill = connection.Stroke
		circleEl.ClassName = "connection"
		circleEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			circleEl.Cx = radius + strokeWidth/2
		} else {
			circleEl.Cx = radius - strokeWidth/2
		}

		path = circleEl.Render()
	case d2target.CircleArrowhead:
		radius := width / 2

		circleEl := d2themes.NewThemableElement("circle")
		circleEl.Cy = radius
		circleEl.R = radius - strokeWidth
		circleEl.Fill = d2target.BG_COLOR
		circleEl.Stroke = connection.Stroke
		circleEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)

		if isTarget {
			circleEl.Cx = radius + strokeWidth/2
		} else {
			circleEl.Cx = radius - strokeWidth/2
		}

		path = circleEl.Render()
	case d2target.CfOne, d2target.CfMany, d2target.CfOneRequired, d2target.CfManyRequired:
		offset := 3.0 + float64(connection.StrokeWidth)*1.8

		var modifierEl *d2themes.ThemableElement
		if arrowhead == d2target.CfOneRequired || arrowhead == d2target.CfManyRequired {
			modifierEl = d2themes.NewThemableElement("path")
			modifierEl.D = fmt.Sprintf("M%f,%f %f,%f",
				offset, 0.,
				offset, height,
			)
			modifierEl.Fill = d2target.BG_COLOR
			modifierEl.Stroke = connection.Stroke
			modifierEl.ClassName = "connection"
			modifierEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)
		} else {
			modifierEl = d2themes.NewThemableElement("circle")
			modifierEl.Cx = offset/2.0 + 2.0
			modifierEl.Cy = height / 2.0
			modifierEl.R = offset / 2.0
			modifierEl.Fill = d2target.BG_COLOR
			modifierEl.Stroke = connection.Stroke
			modifierEl.ClassName = "connection"
			modifierEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)
		}

		childPathEl := d2themes.NewThemableElement("path")
		if arrowhead == d2target.CfMany || arrowhead == d2target.CfManyRequired {
			childPathEl.D = fmt.Sprintf("M%f,%f %f,%f M%f,%f %f,%f M%f,%f %f,%f",
				width-3.0, height/2.0,
				width+offset, height/2.0,
				offset+3.0, height/2.0,
				width+offset, 0.,
				offset+3.0, height/2.0,
				width+offset, height,
			)
		} else {
			childPathEl.D = fmt.Sprintf("M%f,%f %f,%f M%f,%f %f,%f",
				width-3.0, height/2.0,
				width+offset, height/2.0,
				offset*2.0, 0.,
				offset*2.0, height,
			)
		}

		gEl := d2themes.NewThemableElement("g")
		if !isTarget {
			gEl.Transform = fmt.Sprintf("scale(-1) translate(-%f, -%f)", width, height)
		}
		gEl.Fill = d2target.BG_COLOR
		gEl.Stroke = connection.Stroke
		gEl.ClassName = "connection"
		gEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, connection.StrokeWidth)
		gEl.Content = fmt.Sprintf("%s%s",
			modifierEl.Render(), childPathEl.Render(),
		)
		path = gEl.Render()
	default:
		return ""
	}

	var refX float64
	refY := height / 2
	switch arrowhead {
	case d2target.DiamondArrowhead:
		if isTarget {
			refX = width - 0.6*strokeWidth
		} else {
			refX = width/8 + 0.6*strokeWidth
		}
		width *= 1.1
	default:
		if isTarget {
			refX = width - 1.5*strokeWidth
		} else {
			refX = 1.5 * strokeWidth
		}
	}

	return strings.Join([]string{
		fmt.Sprintf(`<marker id="%s" markerWidth="%f" markerHeight="%f" refX="%f" refY="%f"`,
			id, width, height, refX, refY,
		),
		fmt.Sprintf(`viewBox="%f %f %f %f"`, 0., 0., width, height),
		`orient="auto" markerUnits="userSpaceOnUse">`,
		path,
		"</marker>",
	}, " ")
}

// compute the (dx, dy) adjustment to apply to get the arrowhead-adjusted end point
func arrowheadAdjustment(start, end *geo.Point, arrowhead d2target.Arrowhead, edgeStrokeWidth, shapeStrokeWidth int) *geo.Point {
	distance := (float64(edgeStrokeWidth) + float64(shapeStrokeWidth)) / 2.0
	if arrowhead != d2target.NoArrowhead {
		distance += float64(edgeStrokeWidth)
	}

	v := geo.NewVector(end.X-start.X, end.Y-start.Y)
	return v.Unit().Multiply(-distance).ToPoint()
}

func getArrowheadAdjustments(connection d2target.Connection, idToShape map[string]d2target.Shape) (srcAdj, dstAdj *geo.Point) {
	route := connection.Route
	srcShape := idToShape[connection.Src]
	dstShape := idToShape[connection.Dst]

	sourceAdjustment := arrowheadAdjustment(route[1], route[0], connection.SrcArrow, connection.StrokeWidth, srcShape.StrokeWidth)

	targetAdjustment := arrowheadAdjustment(route[len(route)-2], route[len(route)-1], connection.DstArrow, connection.StrokeWidth, dstShape.StrokeWidth)
	return sourceAdjustment, targetAdjustment
}

// returns the path's d attribute for the given connection
func pathData(connection d2target.Connection, srcAdj, dstAdj *geo.Point) string {
	var path []string
	route := connection.Route

	path = append(path, fmt.Sprintf("M %f %f",
		route[0].X+srcAdj.X,
		route[0].Y+srcAdj.Y,
	))

	if connection.IsCurve {
		i := 1
		for ; i < len(route)-3; i += 3 {
			path = append(path, fmt.Sprintf("C %f %f %f %f %f %f",
				route[i].X, route[i].Y,
				route[i+1].X, route[i+1].Y,
				route[i+2].X, route[i+2].Y,
			))
		}
		// final curve target adjustment
		path = append(path, fmt.Sprintf("C %f %f %f %f %f %f",
			route[i].X, route[i].Y,
			route[i+1].X, route[i+1].Y,
			route[i+2].X+dstAdj.X,
			route[i+2].Y+dstAdj.Y,
		))
	} else {
		for i := 1; i < len(route)-1; i++ {
			prevSource := route[i-1]
			prevTarget := route[i]
			currTarget := route[i+1]
			prevVector := prevSource.VectorTo(prevTarget)
			currVector := prevTarget.VectorTo(currTarget)

			dist := geo.EuclideanDistance(prevTarget.X, prevTarget.Y, currTarget.X, currTarget.Y)

			connectionBorderRadius := connection.BorderRadius
			units := math.Min(connectionBorderRadius, dist/2)

			prevTranslations := prevVector.Unit().Multiply(units).ToPoint()
			currTranslations := currVector.Unit().Multiply(units).ToPoint()

			path = append(path, fmt.Sprintf("L %f %f",
				prevTarget.X-prevTranslations.X,
				prevTarget.Y-prevTranslations.Y,
			))

			// If the segment length is too small, instead of drawing 2 arcs, just skip this segment and bezier curve to the next one
			if units < connectionBorderRadius && i < len(route)-2 {
				nextTarget := route[i+2]
				nextVector := geo.NewVector(nextTarget.X-currTarget.X, nextTarget.Y-currTarget.Y)
				i++
				nextTranslations := nextVector.Unit().Multiply(units).ToPoint()

				// These 2 bezier control points aren't just at the corner -- they are reflected at the corner, which causes the curve to be ~tangent to the corner,
				// which matches how the two arcs look
				path = append(path, fmt.Sprintf("C %f %f %f %f %f %f",
					// Control point
					prevTarget.X+prevTranslations.X,
					prevTarget.Y+prevTranslations.Y,
					// Control point
					currTarget.X-nextTranslations.X,
					currTarget.Y-nextTranslations.Y,
					// Where curve ends
					currTarget.X+nextTranslations.X,
					currTarget.Y+nextTranslations.Y,
				))
			} else {
				path = append(path, fmt.Sprintf("S %f %f %f %f",
					prevTarget.X,
					prevTarget.Y,
					prevTarget.X+currTranslations.X,
					prevTarget.Y+currTranslations.Y,
				))
			}
		}

		lastPoint := route[len(route)-1]
		path = append(path, fmt.Sprintf("L %f %f",
			lastPoint.X+dstAdj.X,
			lastPoint.Y+dstAdj.Y,
		))
	}

	return strings.Join(path, " ")
}

func makeLabelMask(labelTL *geo.Point, width, height int) string {
	return fmt.Sprintf(`<rect x="%f" y="%f" width="%d" height="%d" fill="black"></rect>`,
		labelTL.X, labelTL.Y,
		width,
		height,
	)
}

func drawConnection(writer io.Writer, labelMaskID string, connection d2target.Connection, markers map[string]struct{}, idToShape map[string]d2target.Shape, sketchRunner *d2sketch.Runner) (labelMask string, _ error) {
	opacityStyle := ""
	if connection.Opacity != 1.0 {
		opacityStyle = fmt.Sprintf(" style='opacity:%f'", connection.Opacity)
	}
	fmt.Fprintf(writer, `<g id="%s"%s>`, svg.EscapeText(connection.ID), opacityStyle)
	var markerStart string
	if connection.SrcArrow != d2target.NoArrowhead {
		id := arrowheadMarkerID(false, connection)
		if _, in := markers[id]; !in {
			marker := arrowheadMarker(false, id, connection)
			if marker == "" {
				panic(fmt.Sprintf("received empty arrow head marker for: %#v", connection))
			}
			fmt.Fprint(writer, marker)
			markers[id] = struct{}{}
		}
		markerStart = fmt.Sprintf(`marker-start="url(#%s)" `, id)
	}

	var markerEnd string
	if connection.DstArrow != d2target.NoArrowhead {
		id := arrowheadMarkerID(true, connection)
		if _, in := markers[id]; !in {
			marker := arrowheadMarker(true, id, connection)
			if marker == "" {
				panic(fmt.Sprintf("received empty arrow head marker for: %#v", connection))
			}
			fmt.Fprint(writer, marker)
			markers[id] = struct{}{}
		}
		markerEnd = fmt.Sprintf(`marker-end="url(#%s)" `, id)
	}

	var labelTL *geo.Point
	if connection.Label != "" {
		labelTL = connection.GetLabelTopLeft()
		labelTL.X = math.Round(labelTL.X)
		labelTL.Y = math.Round(labelTL.Y)

		if label.Position(connection.LabelPosition).IsOnEdge() {
			labelMask = makeLabelMask(labelTL, connection.LabelWidth, connection.LabelHeight)
		}
	}

	srcAdj, dstAdj := getArrowheadAdjustments(connection, idToShape)
	path := pathData(connection, srcAdj, dstAdj)
	mask := fmt.Sprintf(`mask="url(#%s)"`, labelMaskID)
	if sketchRunner != nil {
		out, err := d2sketch.Connection(sketchRunner, connection, path, mask)
		if err != nil {
			return "", err
		}
		fmt.Fprint(writer, out)

		// render sketch arrowheads separately
		arrowPaths, err := d2sketch.Arrowheads(sketchRunner, connection, srcAdj, dstAdj)
		if err != nil {
			return "", err
		}
		fmt.Fprint(writer, arrowPaths)
	} else {
		animatedClass := ""
		if connection.Animated {
			animatedClass = " animated-connection"
		}

		pathEl := d2themes.NewThemableElement("path")
		pathEl.D = path
		pathEl.Fill = color.None
		pathEl.Stroke = connection.Stroke
		pathEl.ClassName = fmt.Sprintf("connection%s", animatedClass)
		pathEl.Style = connection.CSSStyle()
		pathEl.Attributes = fmt.Sprintf("%s%s%s", markerStart, markerEnd, mask)
		fmt.Fprint(writer, pathEl.Render())
	}

	if connection.Label != "" {
		fontClass := "text"
		if connection.Bold {
			fontClass += "-bold"
		} else if connection.Italic {
			fontClass += "-italic"
		}
		if connection.Fill != color.Empty {
			rectEl := d2themes.NewThemableElement("rect")
			rectEl.X, rectEl.Y = labelTL.X, labelTL.Y
			rectEl.Width, rectEl.Height = float64(connection.LabelWidth), float64(connection.LabelHeight)
			rectEl.Fill = connection.Fill
			fmt.Fprint(writer, rectEl.Render())
		}

		textEl := d2themes.NewThemableElement("text")
		textEl.X = labelTL.X + float64(connection.LabelWidth)/2
		textEl.Y = labelTL.Y + float64(connection.FontSize)
		textEl.Fill = connection.GetFontColor()
		textEl.ClassName = fontClass
		textEl.Style = fmt.Sprintf("text-anchor:%s;font-size:%vpx", "middle", connection.FontSize)
		textEl.Content = RenderText(connection.Label, textEl.X, float64(connection.LabelHeight))
		fmt.Fprint(writer, textEl.Render())
	}

	length := geo.Route(connection.Route).Length()
	if connection.SrcLabel != "" {
		// TODO use arrowhead label dimensions https://github.com/terrastruct/d2/issues/183
		size := float64(connection.FontSize)
		position := 0.
		if length > 0 {
			position = size / length
		}
		fmt.Fprint(writer, renderArrowheadLabel(connection, connection.SrcLabel, position, size, size))
	}
	if connection.DstLabel != "" {
		// TODO use arrowhead label dimensions https://github.com/terrastruct/d2/issues/183
		size := float64(connection.FontSize)
		position := 1.
		if length > 0 {
			position -= size / length
		}
		fmt.Fprint(writer, renderArrowheadLabel(connection, connection.DstLabel, position, size, size))
	}
	fmt.Fprintf(writer, `</g>`)
	return
}

func renderArrowheadLabel(connection d2target.Connection, text string, position, width, height float64) string {
	labelTL := label.UnlockedTop.GetPointOnRoute(connection.Route, float64(connection.StrokeWidth), position, width, height)

	textEl := d2themes.NewThemableElement("text")
	textEl.X = labelTL.X + width/2
	textEl.Y = labelTL.Y + float64(connection.FontSize)
	textEl.Fill = d2target.FG_COLOR
	textEl.ClassName = "text-italic"
	textEl.Style = fmt.Sprintf("text-anchor:%s;font-size:%vpx", "middle", connection.FontSize)
	textEl.Content = RenderText(text, textEl.X, height)
	return textEl.Render()
}

func renderOval(tl *geo.Point, width, height float64, fill, stroke, style string) string {
	el := d2themes.NewThemableElement("ellipse")
	el.Rx = width / 2
	el.Ry = height / 2
	el.Cx = tl.X + el.Rx
	el.Cy = tl.Y + el.Ry
	el.Fill, el.Stroke = fill, stroke
	el.ClassName = "shape"
	el.Style = style
	return el.Render()
}

func renderDoubleOval(tl *geo.Point, width, height float64, fill, stroke, style string) string {
	var innerTL *geo.Point = tl.AddVector(geo.NewVector(d2target.INNER_BORDER_OFFSET, d2target.INNER_BORDER_OFFSET))
	return renderOval(tl, width, height, fill, stroke, style) + renderOval(innerTL, width-10, height-10, fill, stroke, style)
}

func defineShadowFilter(writer io.Writer) {
	fmt.Fprint(writer, `<defs>
	<filter id="shadow-filter" width="200%" height="200%" x="-50%" y="-50%">
		<feGaussianBlur stdDeviation="1.7 " in="SourceGraphic"></feGaussianBlur>
		<feFlood flood-color="#3d4574" flood-opacity="0.4" result="ShadowFeFlood" in="SourceGraphic"></feFlood>
		<feComposite in="ShadowFeFlood" in2="SourceAlpha" operator="in" result="ShadowFeComposite"></feComposite>
		<feOffset dx="3" dy="5" result="ShadowFeOffset" in="ShadowFeComposite"></feOffset>
		<feBlend in="SourceGraphic" in2="ShadowFeOffset" mode="normal" result="ShadowFeBlend"></feBlend>
	</filter>
</defs>`)
}

func render3dRect(targetShape d2target.Shape) string {
	moveTo := func(p d2target.Point) string {
		return fmt.Sprintf("M%d,%d", p.X+targetShape.Pos.X, p.Y+targetShape.Pos.Y)
	}
	lineTo := func(p d2target.Point) string {
		return fmt.Sprintf("L%d,%d", p.X+targetShape.Pos.X, p.Y+targetShape.Pos.Y)
	}

	// draw border all in one path to prevent overlapping sections
	var borderSegments []string
	borderSegments = append(borderSegments,
		moveTo(d2target.Point{X: 0, Y: 0}),
	)
	for _, v := range []d2target.Point{
		{X: d2target.THREE_DEE_OFFSET, Y: -d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: -d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: targetShape.Height - d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width, Y: targetShape.Height},
		{X: 0, Y: targetShape.Height},
		{X: 0, Y: 0},
		{X: targetShape.Width, Y: 0},
		{X: targetShape.Width, Y: targetShape.Height},
	} {
		borderSegments = append(borderSegments, lineTo(v))
	}
	// move to top right to draw last segment without overlapping
	borderSegments = append(borderSegments,
		moveTo(d2target.Point{X: targetShape.Width, Y: 0}),
	)
	borderSegments = append(borderSegments,
		lineTo(d2target.Point{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: -d2target.THREE_DEE_OFFSET}),
	)
	border := d2themes.NewThemableElement("path")
	border.D = strings.Join(borderSegments, " ")
	border.Fill = color.None
	_, borderStroke := d2themes.ShapeTheme(targetShape)
	border.Stroke = borderStroke
	borderStyle := targetShape.CSSStyle()
	border.Style = borderStyle
	renderedBorder := border.Render()

	// create mask from border stroke, to cut away from the shape fills
	maskID := fmt.Sprintf("border-mask-%v", svg.EscapeText(targetShape.ID))
	borderMask := strings.Join([]string{
		fmt.Sprintf(`<defs><mask id="%s" maskUnits="userSpaceOnUse" x="%d" y="%d" width="%d" height="%d">`,
			maskID, targetShape.Pos.X, targetShape.Pos.Y-d2target.THREE_DEE_OFFSET, targetShape.Width+d2target.THREE_DEE_OFFSET, targetShape.Height+d2target.THREE_DEE_OFFSET,
		),
		fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="white"></rect>`,
			targetShape.Pos.X, targetShape.Pos.Y-d2target.THREE_DEE_OFFSET, targetShape.Width+d2target.THREE_DEE_OFFSET, targetShape.Height+d2target.THREE_DEE_OFFSET,
		),
		fmt.Sprintf(`<path d="%s" style="%s;stroke:#000;fill:none;opacity:1;"/></mask></defs>`,
			strings.Join(borderSegments, ""), borderStyle),
	}, "\n")

	// render the main rectangle without stroke and the border mask
	mainShape := d2themes.NewThemableElement("rect")
	mainShape.X = float64(targetShape.Pos.X)
	mainShape.Y = float64(targetShape.Pos.Y)
	mainShape.Width = float64(targetShape.Width)
	mainShape.Height = float64(targetShape.Height)
	mainShape.SetMaskUrl(maskID)
	mainShapeFill, _ := d2themes.ShapeTheme(targetShape)
	mainShape.Fill = mainShapeFill
	mainShape.Stroke = color.None
	mainShape.Style = targetShape.CSSStyle()
	mainShapeRendered := mainShape.Render()

	// render the side shapes in the darkened color without stroke and the border mask
	var sidePoints []string
	for _, v := range []d2target.Point{
		{X: 0, Y: 0},
		{X: d2target.THREE_DEE_OFFSET, Y: -d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: -d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: targetShape.Height - d2target.THREE_DEE_OFFSET},
		{X: targetShape.Width, Y: targetShape.Height},
		{X: targetShape.Width, Y: 0},
	} {
		sidePoints = append(sidePoints,
			fmt.Sprintf("%d,%d", v.X+targetShape.Pos.X, v.Y+targetShape.Pos.Y),
		)
	}
	darkerColor, err := color.Darken(targetShape.Fill)
	if err != nil {
		darkerColor = targetShape.Fill
	}
	sideShape := d2themes.NewThemableElement("polygon")
	sideShape.Fill = darkerColor
	sideShape.Points = strings.Join(sidePoints, " ")
	sideShape.SetMaskUrl(maskID)
	sideShape.Style = targetShape.CSSStyle()
	renderedSides := sideShape.Render()

	return borderMask + mainShapeRendered + renderedSides + renderedBorder
}

func render3dHexagon(targetShape d2target.Shape) string {
	moveTo := func(p d2target.Point) string {
		return fmt.Sprintf("M%d,%d", p.X+targetShape.Pos.X, p.Y+targetShape.Pos.Y)
	}
	lineTo := func(p d2target.Point) string {
		return fmt.Sprintf("L%d,%d", p.X+targetShape.Pos.X, p.Y+targetShape.Pos.Y)
	}
	scale := func(n int, f float64) int {
		return int(float64(n) * f)
	}
	halfYFactor := 43.6 / 87.3

	// draw border all in one path to prevent overlapping sections
	var borderSegments []string
	// start from the top-left
	borderSegments = append(borderSegments,
		moveTo(d2target.Point{X: scale(targetShape.Width, 0.25), Y: 0}),
	)
	Y_OFFSET := d2target.THREE_DEE_OFFSET / 2
	// The following iterates through the sidepoints in clockwise order from top-left, then the main points in clockwise order from bottom-right
	for _, v := range []d2target.Point{
		{X: scale(targetShape.Width, 0.25) + d2target.THREE_DEE_OFFSET, Y: -Y_OFFSET},
		{X: scale(targetShape.Width, 0.75) + d2target.THREE_DEE_OFFSET, Y: -Y_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: scale(targetShape.Height, halfYFactor) - Y_OFFSET},
		{X: scale(targetShape.Width, 0.75) + d2target.THREE_DEE_OFFSET, Y: targetShape.Height - Y_OFFSET},
		{X: scale(targetShape.Width, 0.75), Y: targetShape.Height},
		{X: scale(targetShape.Width, 0.25), Y: targetShape.Height},
		{X: 0, Y: scale(targetShape.Height, halfYFactor)},
		{X: scale(targetShape.Width, 0.25), Y: 0},
		{X: scale(targetShape.Width, 0.75), Y: 0},
		{X: targetShape.Width, Y: scale(targetShape.Height, halfYFactor)},
		{X: scale(targetShape.Width, 0.75), Y: targetShape.Height},
	} {
		borderSegments = append(borderSegments, lineTo(v))
	}
	for _, v := range []d2target.Point{
		{X: scale(targetShape.Width, 0.75), Y: 0},
		{X: targetShape.Width, Y: scale(targetShape.Height, halfYFactor)},
		{X: scale(targetShape.Width, 0.75), Y: targetShape.Height},
	} {
		borderSegments = append(borderSegments, moveTo(v))
		borderSegments = append(borderSegments, lineTo(
			d2target.Point{X: v.X + d2target.THREE_DEE_OFFSET, Y: v.Y - Y_OFFSET},
		))
	}
	border := d2themes.NewThemableElement("path")
	border.D = strings.Join(borderSegments, " ")
	border.Fill = color.None
	_, borderStroke := d2themes.ShapeTheme(targetShape)
	border.Stroke = borderStroke
	borderStyle := targetShape.CSSStyle()
	border.Style = borderStyle
	renderedBorder := border.Render()

	var mainPoints []string
	for _, v := range []d2target.Point{
		{X: scale(targetShape.Width, 0.25), Y: 0},
		{X: scale(targetShape.Width, 0.75), Y: 0},
		{X: targetShape.Width, Y: scale(targetShape.Height, halfYFactor)},
		{X: scale(targetShape.Width, 0.75), Y: targetShape.Height},
		{X: scale(targetShape.Width, 0.25), Y: targetShape.Height},
		{X: 0, Y: scale(targetShape.Height, halfYFactor)},
	} {
		mainPoints = append(mainPoints,
			fmt.Sprintf("%d,%d", v.X+targetShape.Pos.X, v.Y+targetShape.Pos.Y),
		)
	}

	mainPointsPoly := strings.Join(mainPoints, " ")
	// create mask from border stroke, to cut away from the shape fills
	maskID := fmt.Sprintf("border-mask-%v", svg.EscapeText(targetShape.ID))
	borderMask := strings.Join([]string{
		fmt.Sprintf(`<defs><mask id="%s" maskUnits="userSpaceOnUse" x="%d" y="%d" width="%d" height="%d">`,
			maskID, targetShape.Pos.X, targetShape.Pos.Y-d2target.THREE_DEE_OFFSET, targetShape.Width+d2target.THREE_DEE_OFFSET, targetShape.Height+d2target.THREE_DEE_OFFSET,
		),
		fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="white"></rect>`,
			targetShape.Pos.X, targetShape.Pos.Y-d2target.THREE_DEE_OFFSET, targetShape.Width+d2target.THREE_DEE_OFFSET, targetShape.Height+d2target.THREE_DEE_OFFSET,
		),
		fmt.Sprintf(`<path d="%s" style="%s;stroke:#000;fill:none;opacity:1;"/></mask></defs>`,
			strings.Join(borderSegments, ""), borderStyle),
	}, "\n")
	// render the main hexagon without stroke and the border mask
	mainShape := d2themes.NewThemableElement("polygon")
	mainShape.X = float64(targetShape.Pos.X)
	mainShape.Y = float64(targetShape.Pos.Y)
	mainShape.Points = mainPointsPoly
	mainShape.SetMaskUrl(maskID)
	mainShapeFill, _ := d2themes.ShapeTheme(targetShape)
	mainShape.Fill = mainShapeFill
	mainShape.Stroke = color.None
	mainShape.Style = targetShape.CSSStyle()
	mainShapeRendered := mainShape.Render()

	// render the side shapes in the darkened color without stroke and the border mask
	var sidePoints []string
	for _, v := range []d2target.Point{
		{X: scale(targetShape.Width, 0.25) + d2target.THREE_DEE_OFFSET, Y: -Y_OFFSET},
		{X: scale(targetShape.Width, 0.75) + d2target.THREE_DEE_OFFSET, Y: -Y_OFFSET},
		{X: targetShape.Width + d2target.THREE_DEE_OFFSET, Y: scale(targetShape.Height, halfYFactor) - Y_OFFSET},
		{X: scale(targetShape.Width, 0.75) + d2target.THREE_DEE_OFFSET, Y: targetShape.Height - Y_OFFSET},
		{X: scale(targetShape.Width, 0.75), Y: targetShape.Height},
		{X: targetShape.Width, Y: scale(targetShape.Height, halfYFactor)},
		{X: scale(targetShape.Width, 0.75), Y: 0},
		{X: scale(targetShape.Width, 0.25), Y: 0},
	} {
		sidePoints = append(sidePoints,
			fmt.Sprintf("%d,%d", v.X+targetShape.Pos.X, v.Y+targetShape.Pos.Y),
		)
	}
	// TODO make darker color part of the theme? or just keep this bypass
	darkerColor, err := color.Darken(targetShape.Fill)
	if err != nil {
		darkerColor = targetShape.Fill
	}
	sideShape := d2themes.NewThemableElement("polygon")
	sideShape.Fill = darkerColor
	sideShape.Points = strings.Join(sidePoints, " ")
	sideShape.SetMaskUrl(maskID)
	sideShape.Style = targetShape.CSSStyle()
	renderedSides := sideShape.Render()

	return borderMask + mainShapeRendered + renderedSides + renderedBorder
}

func drawShape(writer io.Writer, targetShape d2target.Shape, sketchRunner *d2sketch.Runner) (labelMask string, err error) {
	closingTag := "</g>"
	if targetShape.Link != "" {

		fmt.Fprintf(writer, `<a href="%s" xlink:href="%[1]s">`, svg.EscapeText(targetShape.Link))
		closingTag += "</a>"
	}
	// Opacity is a unique style, it applies to everything for a shape
	opacityStyle := ""
	if targetShape.Opacity != 1.0 {
		opacityStyle = fmt.Sprintf(" style='opacity:%f'", targetShape.Opacity)
	}
	fmt.Fprintf(writer, `<g id="%s"%s>`, svg.EscapeText(targetShape.ID), opacityStyle)
	tl := geo.NewPoint(float64(targetShape.Pos.X), float64(targetShape.Pos.Y))
	width := float64(targetShape.Width)
	height := float64(targetShape.Height)
	fill, stroke := d2themes.ShapeTheme(targetShape)
	style := targetShape.CSSStyle()
	shapeType := d2target.DSL_SHAPE_TO_SHAPE_TYPE[targetShape.Type]

	s := shape.NewShape(shapeType, geo.NewBox(tl, width, height))

	var shadowAttr string
	if targetShape.Shadow {
		switch targetShape.Type {
		case d2target.ShapeText,
			d2target.ShapeCode,
			d2target.ShapeClass,
			d2target.ShapeSQLTable:
		default:
			shadowAttr = `filter="url(#shadow-filter)" `
		}
	}

	var blendModeClass string
	if targetShape.Blend {
		blendModeClass = " blend"
	}

	fmt.Fprintf(writer, `<g class="shape%s" %s>`, blendModeClass, shadowAttr)

	var multipleTL *geo.Point
	if targetShape.Multiple {
		multipleTL = tl.AddVector(multipleOffset)
	}

	switch targetShape.Type {
	case d2target.ShapeClass:
		if sketchRunner != nil {
			out, err := d2sketch.Class(sketchRunner, targetShape)
			if err != nil {
				return "", err
			}
			fmt.Fprint(writer, out)
		} else {
			drawClass(writer, targetShape)
		}
		addAppendixItems(writer, targetShape)
		fmt.Fprint(writer, `</g>`)
		fmt.Fprint(writer, closingTag)
		return labelMask, nil
	case d2target.ShapeSQLTable:
		if sketchRunner != nil {
			out, err := d2sketch.Table(sketchRunner, targetShape)
			if err != nil {
				return "", err
			}
			fmt.Fprint(writer, out)
		} else {
			drawTable(writer, targetShape)
		}
		addAppendixItems(writer, targetShape)
		fmt.Fprint(writer, `</g>`)
		fmt.Fprint(writer, closingTag)
		return labelMask, nil
	case d2target.ShapeOval:
		if targetShape.DoubleBorder {
			if targetShape.Multiple {
				fmt.Fprint(writer, renderDoubleOval(multipleTL, width, height, fill, stroke, style))
			}
			if sketchRunner != nil {
				out, err := d2sketch.DoubleOval(sketchRunner, targetShape)
				if err != nil {
					return "", err
				}
				fmt.Fprint(writer, out)
			} else {
				fmt.Fprint(writer, renderDoubleOval(tl, width, height, fill, stroke, style))
			}
		} else {
			if targetShape.Multiple {
				fmt.Fprint(writer, renderOval(multipleTL, width, height, fill, stroke, style))
			}
			if sketchRunner != nil {
				out, err := d2sketch.Oval(sketchRunner, targetShape)
				if err != nil {
					return "", err
				}
				fmt.Fprint(writer, out)
			} else {
				fmt.Fprint(writer, renderOval(tl, width, height, fill, stroke, style))
			}
		}

	case d2target.ShapeImage:
		el := d2themes.NewThemableElement("image")
		el.X = float64(targetShape.Pos.X)
		el.Y = float64(targetShape.Pos.Y)
		el.Width = float64(targetShape.Width)
		el.Height = float64(targetShape.Height)
		el.Href = html.EscapeString(targetShape.Icon.String())
		el.Fill = fill
		el.Stroke = stroke
		el.Style = style
		fmt.Fprint(writer, el.Render())

	// TODO should standardize "" to rectangle
	case d2target.ShapeRectangle, d2target.ShapeSequenceDiagram, "":
		// TODO use Rx property of NewThemableElement instead
		// DO
		rx := ""
		if targetShape.BorderRadius != 0 {
			rx = fmt.Sprintf(` rx="%d"`, targetShape.BorderRadius)
		}
		if targetShape.ThreeDee {
			fmt.Fprint(writer, render3dRect(targetShape))
		} else {
			if !targetShape.DoubleBorder {
				if targetShape.Multiple {
					el := d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X + 10)
					el.Y = float64(targetShape.Pos.Y - 10)
					el.Width = float64(targetShape.Width)
					el.Height = float64(targetShape.Height)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())
				}
				if sketchRunner != nil {
					out, err := d2sketch.Rect(sketchRunner, targetShape)
					if err != nil {
						return "", err
					}
					fmt.Fprint(writer, out)
				} else {
					el := d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X)
					el.Y = float64(targetShape.Pos.Y)
					el.Width = float64(targetShape.Width)
					el.Height = float64(targetShape.Height)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())
				}
			} else {
				if targetShape.Multiple {
					el := d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X + 10)
					el.Y = float64(targetShape.Pos.Y - 10)
					el.Width = float64(targetShape.Width)
					el.Height = float64(targetShape.Height)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())

					el = d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X + 10 + d2target.INNER_BORDER_OFFSET)
					el.Y = float64(targetShape.Pos.Y - 10 + d2target.INNER_BORDER_OFFSET)
					el.Width = float64(targetShape.Width - 2*d2target.INNER_BORDER_OFFSET)
					el.Height = float64(targetShape.Height - 2*d2target.INNER_BORDER_OFFSET)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())
				}
				if sketchRunner != nil {
					out, err := d2sketch.DoubleRect(sketchRunner, targetShape)
					if err != nil {
						return "", err
					}
					fmt.Fprint(writer, out)
				} else {
					el := d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X)
					el.Y = float64(targetShape.Pos.Y)
					el.Width = float64(targetShape.Width)
					el.Height = float64(targetShape.Height)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())

					el = d2themes.NewThemableElement("rect")
					el.X = float64(targetShape.Pos.X + d2target.INNER_BORDER_OFFSET)
					el.Y = float64(targetShape.Pos.Y + d2target.INNER_BORDER_OFFSET)
					el.Width = float64(targetShape.Width - 2*d2target.INNER_BORDER_OFFSET)
					el.Height = float64(targetShape.Height - 2*d2target.INNER_BORDER_OFFSET)
					el.Fill = fill
					el.Stroke = stroke
					el.Style = style
					el.Attributes = rx
					fmt.Fprint(writer, el.Render())
				}
			}
		}
	case d2target.ShapeHexagon:
		if targetShape.ThreeDee {
			fmt.Fprint(writer, render3dHexagon(targetShape))
		} else {
			if targetShape.Multiple {
				multiplePathData := shape.NewShape(shapeType, geo.NewBox(multipleTL, width, height)).GetSVGPathData()
				el := d2themes.NewThemableElement("path")
				el.Fill = fill
				el.Stroke = stroke
				el.Style = style
				for _, pathData := range multiplePathData {
					el.D = pathData
					fmt.Fprint(writer, el.Render())
				}
			}

			if sketchRunner != nil {
				out, err := d2sketch.Paths(sketchRunner, targetShape, s.GetSVGPathData())
				if err != nil {
					return "", err
				}
				fmt.Fprint(writer, out)
			} else {
				el := d2themes.NewThemableElement("path")
				el.Fill = fill
				el.Stroke = stroke
				el.Style = style
				for _, pathData := range s.GetSVGPathData() {
					el.D = pathData
					fmt.Fprint(writer, el.Render())
				}
			}
		}
	case d2target.ShapeText, d2target.ShapeCode:
	default:
		if targetShape.Multiple {
			multiplePathData := shape.NewShape(shapeType, geo.NewBox(multipleTL, width, height)).GetSVGPathData()
			el := d2themes.NewThemableElement("path")
			el.Fill = fill
			el.Stroke = stroke
			el.Style = style
			for _, pathData := range multiplePathData {
				el.D = pathData
				fmt.Fprint(writer, el.Render())
			}
		}

		if sketchRunner != nil {
			out, err := d2sketch.Paths(sketchRunner, targetShape, s.GetSVGPathData())
			if err != nil {
				return "", err
			}
			fmt.Fprint(writer, out)
		} else {
			el := d2themes.NewThemableElement("path")
			el.Fill = fill
			el.Stroke = stroke
			el.Style = style
			for _, pathData := range s.GetSVGPathData() {
				el.D = pathData
				fmt.Fprint(writer, el.Render())
			}
		}
	}

	// to examine GetInsidePlacement
	// padX, padY := s.GetDefaultPadding()
	// innerTL := s.GetInsidePlacement(s.GetInnerBox().Width, s.GetInnerBox().Height, padX, padY)
	// fmt.Fprint(writer, renderOval(&innerTL, 5, 5, "fill:red;"))

	// Closes the class=shape
	fmt.Fprint(writer, `</g>`)

	if targetShape.Icon != nil && targetShape.Type != d2target.ShapeImage {
		iconPosition := label.Position(targetShape.IconPosition)
		var box *geo.Box
		if iconPosition.IsOutside() {
			box = s.GetBox()
		} else {
			box = s.GetInnerBox()
		}
		iconSize := d2target.GetIconSize(box, targetShape.IconPosition)

		tl := iconPosition.GetPointOnBox(box, label.PADDING, float64(iconSize), float64(iconSize))

		fmt.Fprintf(writer, `<image href="%s" x="%f" y="%f" width="%d" height="%d" />`,
			html.EscapeString(targetShape.Icon.String()),
			tl.X,
			tl.Y,
			iconSize,
			iconSize,
		)
	}

	if targetShape.Label != "" {
		labelPosition := label.Position(targetShape.LabelPosition)
		var box *geo.Box
		if labelPosition.IsOutside() {
			box = s.GetBox()
		} else {
			box = s.GetInnerBox()
		}
		labelTL := labelPosition.GetPointOnBox(box, label.PADDING,
			float64(targetShape.LabelWidth),
			float64(targetShape.LabelHeight),
		)

		fontClass := "text"
		if targetShape.Bold {
			fontClass += "-bold"
		} else if targetShape.Italic {
			fontClass += "-italic"
		}
		if targetShape.Underline {
			fontClass += " text-underline"
		}

		if targetShape.Type == d2target.ShapeCode {
			lexer := lexers.Get(targetShape.Language)
			if lexer == nil {
				lexer = lexers.Fallback
			}
			for _, isLight := range []bool{true, false} {
				theme := "github"
				if !isLight {
					theme = "catppuccin-mocha"
				}
				style := styles.Get(theme)
				if style == nil {
					return labelMask, errors.New(`code snippet style "github" not found`)
				}
				formatter := formatters.Get("svg")
				if formatter == nil {
					return labelMask, errors.New(`code snippet formatter "svg" not found`)
				}
				iterator, err := lexer.Tokenise(nil, targetShape.Label)
				if err != nil {
					return labelMask, err
				}

				svgStyles := styleToSVG(style)
				class := "light-code"
				if !isLight {
					class = "dark-code"
				}
				fmt.Fprintf(writer, `<g transform="translate(%f %f)" class="%s">`, box.TopLeft.X, box.TopLeft.Y, class)
				rectEl := d2themes.NewThemableElement("rect")
				rectEl.Width = float64(targetShape.Width)
				rectEl.Height = float64(targetShape.Height)
				rectEl.Stroke = targetShape.Stroke
				rectEl.ClassName = "shape"
				rectEl.Style = fmt.Sprintf(`fill:%s`, style.Get(chroma.Background).Background.String())
				fmt.Fprint(writer, rectEl.Render())
				// Padding
				fmt.Fprint(writer, `<g transform="translate(6 6)">`)

				for index, tokens := range chroma.SplitTokensIntoLines(iterator.Tokens()) {
					// TODO mono font looks better with 1.2 em (use px equivalent), but textmeasure needs to account for it. Not obvious how that should be done
					fmt.Fprintf(writer, "<text class=\"text-mono\" x=\"0\" y=\"%fem\" xml:space=\"preserve\">", 1*float64(index+1))
					for _, token := range tokens {
						text := svgEscaper.Replace(token.String())
						attr := styleAttr(svgStyles, token.Type)
						if attr != "" {
							text = fmt.Sprintf("<tspan %s>%s</tspan>", attr, text)
						}
						fmt.Fprint(writer, text)
					}
					fmt.Fprint(writer, "</text>")
				}
				fmt.Fprint(writer, "</g></g>")
			}
		} else if targetShape.Type == d2target.ShapeText && targetShape.Language == "latex" {
			render, err := d2latex.Render(targetShape.Label)
			if err != nil {
				return labelMask, err
			}
			gEl := d2themes.NewThemableElement("g")
			gEl.SetTranslate(float64(box.TopLeft.X), float64(box.TopLeft.Y))
			gEl.Color = targetShape.Stroke
			gEl.Content = render
			fmt.Fprint(writer, gEl.Render())
		} else if targetShape.Type == d2target.ShapeText && targetShape.Language != "" {
			render, err := textmeasure.RenderMarkdown(targetShape.Label)
			if err != nil {
				return labelMask, err
			}
			fmt.Fprintf(writer, `<g><foreignObject requiredFeatures="http://www.w3.org/TR/SVG11/feature#Extensibility" x="%f" y="%f" width="%d" height="%d">`,
				box.TopLeft.X, box.TopLeft.Y, targetShape.Width, targetShape.Height,
			)
			// we need the self closing form in this svg/xhtml context
			render = strings.ReplaceAll(render, "<hr>", "<hr />")

			mdEl := d2themes.NewThemableElement("div")
			mdEl.ClassName = "md"
			mdEl.Content = render
			fmt.Fprint(writer, mdEl.Render())
			fmt.Fprint(writer, `</foreignObject></g>`)
		} else {
			if targetShape.LabelFill != "" {
				rectEl := d2themes.NewThemableElement("rect")
				rectEl.X = labelTL.X
				rectEl.Y = labelTL.Y
				rectEl.Width = float64(targetShape.LabelWidth)
				rectEl.Height = float64(targetShape.LabelHeight)
				rectEl.Fill = targetShape.LabelFill
				fmt.Fprint(writer, rectEl.Render())
			}
			textEl := d2themes.NewThemableElement("text")
			textEl.X = labelTL.X + float64(targetShape.LabelWidth)/2
			// text is vertically positioned at its baseline which is at labelTL+FontSize
			textEl.Y = labelTL.Y + float64(targetShape.FontSize)
			textEl.Fill = targetShape.GetFontColor()
			textEl.ClassName = fontClass
			textEl.Style = fmt.Sprintf("text-anchor:%s;font-size:%vpx", "middle", targetShape.FontSize)
			textEl.Content = RenderText(targetShape.Label, textEl.X, float64(targetShape.LabelHeight))
			fmt.Fprint(writer, textEl.Render())
			if targetShape.Blend {
				labelMask = makeLabelMask(labelTL, targetShape.LabelWidth, targetShape.LabelHeight-d2graph.INNER_LABEL_PADDING)
			}
		}
	}

	addAppendixItems(writer, targetShape)

	fmt.Fprint(writer, closingTag)
	return labelMask, nil
}

func addAppendixItems(writer io.Writer, shape d2target.Shape) {
	rightPadForTooltip := 0
	if shape.Tooltip != "" {
		rightPadForTooltip = 2 * appendixIconRadius
		fmt.Fprintf(writer, `<g transform="translate(%d %d)" class="appendix-icon">%s</g>`,
			shape.Pos.X+shape.Width-appendixIconRadius,
			shape.Pos.Y-appendixIconRadius,
			TooltipIcon,
		)
		fmt.Fprintf(writer, `<title>%s</title>`, svg.EscapeText(shape.Tooltip))
	}

	if shape.Link != "" {
		fmt.Fprintf(writer, `<g transform="translate(%d %d)" class="appendix-icon">%s</g>`,
			shape.Pos.X+shape.Width-appendixIconRadius-rightPadForTooltip,
			shape.Pos.Y-appendixIconRadius,
			LinkIcon,
		)
	}
}

func RenderText(text string, x, height float64) string {
	if !strings.Contains(text, "\n") {
		return svg.EscapeText(text)
	}
	rendered := []string{}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		dy := height / float64(len(lines))
		if i == 0 {
			dy = 0
		}
		escaped := svg.EscapeText(line)
		if escaped == "" {
			// if there are multiple newlines in a row we still need text for the tspan to render
			escaped = " "
		}
		rendered = append(rendered, fmt.Sprintf(`<tspan x="%f" dy="%f">%s</tspan>`, x, dy, escaped))
	}
	return strings.Join(rendered, "")
}

func embedFonts(buf *bytes.Buffer, source string, fontFamily *d2fonts.FontFamily) {
	fmt.Fprint(buf, `<style type="text/css"><![CDATA[`)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text"`,
			`class="text `,
			`class="md"`,
		},
		fmt.Sprintf(`
.text {
	font-family: "font-regular";
}
@font-face {
	font-family: font-regular;
	src: url("%s");
}`,
			d2fonts.FontEncodings[fontFamily.Font(0, d2fonts.FONT_STYLE_REGULAR)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`text-underline`,
		},
		`
.text-underline {
	text-decoration: underline;
}`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`animated-connection`,
		},
		`
@keyframes dashdraw {
	from {
		stroke-dashoffset: 0;
	}
}
`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`appendix-icon`,
		},
		`
.appendix-icon {
	filter: drop-shadow(0px 0px 32px rgba(31, 36, 58, 0.1));
}`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-bold`,
			`<b>`,
			`<strong>`,
		},
		fmt.Sprintf(`
.text-bold {
	font-family: "font-bold";
}
@font-face {
	font-family: font-bold;
	src: url("%s");
}`,
			d2fonts.FontEncodings[fontFamily.Font(0, d2fonts.FONT_STYLE_BOLD)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-italic`,
			`<em>`,
			`<dfn>`,
		},
		fmt.Sprintf(`
.text-italic {
	font-family: "font-italic";
}
@font-face {
	font-family: font-italic;
	src: url("%s");
}`,
			d2fonts.FontEncodings[fontFamily.Font(0, d2fonts.FONT_STYLE_ITALIC)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-mono`,
			`<pre>`,
			`<code>`,
			`<kbd>`,
			`<samp>`,
		},
		fmt.Sprintf(`
.text-mono {
	font-family: "font-mono";
}
@font-face {
	font-family: font-mono;
	src: url("%s");
}`,
			d2fonts.FontEncodings[d2fonts.SourceCodePro.Font(0, d2fonts.FONT_STYLE_REGULAR)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-mono-bold"`,
		},
		fmt.Sprintf(`
.text-mono-bold {
	font-family: "font-mono-bold";
}
@font-face {
	font-family: font-mono-bold;
	src: url("%s");
}`,
			d2fonts.FontEncodings[d2fonts.SourceCodePro.Font(0, d2fonts.FONT_STYLE_BOLD)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-mono-italic"`,
		},
		fmt.Sprintf(`
.text-mono-italic {
	font-family: "font-mono-italic";
}
@font-face {
	font-family: font-mono-italic;
	src: url("%s");
}`,
			d2fonts.FontEncodings[d2fonts.SourceCodePro.Font(0, d2fonts.FONT_STYLE_ITALIC)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-mono-bold"`,
		},
		fmt.Sprintf(`
.text-mono-bold {
	font-family: "font-mono-bold";
}
@font-face {
	font-family: font-mono-bold;
	src: url("%s");
}`,
			d2fonts.FontEncodings[d2fonts.SourceCodePro.Font(0, d2fonts.FONT_STYLE_BOLD)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`class="text-mono-italic"`,
		},
		fmt.Sprintf(`
.text-mono-italic {
	font-family: "font-mono-italic";
}
@font-face {
	font-family: font-mono-italic;
	src: url("%s");
}`,
			d2fonts.FontEncodings[d2fonts.SourceCodePro.Font(0, d2fonts.FONT_STYLE_ITALIC)],
		),
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`sketch-overlay-bright`,
		},
		`
.sketch-overlay-bright {
	fill: url(#streaks-bright);
	mix-blend-mode: darken;
}`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`sketch-overlay-normal`,
		},
		`
.sketch-overlay-normal {
	fill: url(#streaks-normal);
	mix-blend-mode: color-burn;
}`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`sketch-overlay-dark`,
		},
		`
.sketch-overlay-dark {
	fill: url(#streaks-dark);
	mix-blend-mode: overlay;
}`,
	)

	appendOnTrigger(
		buf,
		source,
		[]string{
			`sketch-overlay-darker`,
		},
		`
.sketch-overlay-darker {
	fill: url(#streaks-darker);
	mix-blend-mode: lighten;
}`,
	)

	fmt.Fprint(buf, `]]></style>`)
}

func appendOnTrigger(buf *bytes.Buffer, source string, triggers []string, newContent string) {
	for _, trigger := range triggers {
		if strings.Contains(source, trigger) {
			fmt.Fprint(buf, newContent)
			break
		}
	}
}

const DEFAULT_THEME int64 = 0

var DEFAULT_DARK_THEME *int64 = nil // no theme selected

func Render(diagram *d2target.Diagram, opts *RenderOpts) ([]byte, error) {
	var sketchRunner *d2sketch.Runner
	pad := DEFAULT_PADDING
	themeID := DEFAULT_THEME
	darkThemeID := DEFAULT_DARK_THEME
	setDimensions := false
	if opts != nil {
		pad = opts.Pad
		if opts.Sketch {
			var err error
			sketchRunner, err = d2sketch.InitSketchVM()
			if err != nil {
				return nil, err
			}
		}
		themeID = opts.ThemeID
		darkThemeID = opts.DarkThemeID
		setDimensions = opts.SetDimensions
	}

	buf := &bytes.Buffer{}

	// only define shadow filter if a shape uses it
	for _, s := range diagram.Shapes {
		if s.Shadow {
			defineShadowFilter(buf)
			break
		}
	}

	// Mask URLs are global. So when multiple SVGs attach to a DOM, they share
	// the same namespace for mask URLs.
	labelMaskID, err := diagram.HashID()
	if err != nil {
		return nil, err
	}

	// SVG has no notion of z-index. The z-index is effectively the order it's drawn.
	// So draw from the least nested to most nested
	idToShape := make(map[string]d2target.Shape)
	allObjects := make([]DiagramObject, 0, len(diagram.Shapes)+len(diagram.Connections))
	for _, s := range diagram.Shapes {
		idToShape[s.ID] = s
		allObjects = append(allObjects, s)
	}
	for _, c := range diagram.Connections {
		allObjects = append(allObjects, c)
	}

	sortObjects(allObjects)

	var labelMasks []string
	markers := map[string]struct{}{}
	for _, obj := range allObjects {
		if c, is := obj.(d2target.Connection); is {
			labelMask, err := drawConnection(buf, labelMaskID, c, markers, idToShape, sketchRunner)
			if err != nil {
				return nil, err
			}
			if labelMask != "" {
				labelMasks = append(labelMasks, labelMask)
			}
		} else if s, is := obj.(d2target.Shape); is {
			labelMask, err := drawShape(buf, s, sketchRunner)
			if err != nil {
				return nil, err
			} else if labelMask != "" {
				labelMasks = append(labelMasks, labelMask)
			}
		} else {
			return nil, fmt.Errorf("unknown object of type %T", obj)
		}
	}

	// Note: we always want this since we reference it on connections even if there end up being no masked labels
	left, top, w, h := dimensions(diagram, pad)
	fmt.Fprint(buf, strings.Join([]string{
		fmt.Sprintf(`<mask id="%s" maskUnits="userSpaceOnUse" x="%d" y="%d" width="%d" height="%d">`,
			labelMaskID, left, top, w, h,
		),
		fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="white"></rect>`,
			left, top, w, h,
		),
		strings.Join(labelMasks, "\n"),
		`</mask>`,
	}, "\n"))

	// generate style elements that will be appended to the SVG tag
	upperBuf := &bytes.Buffer{}
	embedFonts(upperBuf, buf.String(), diagram.FontFamily) // embedFonts *must* run before `d2sketch.DefineFillPatterns`, but after all elements are appended to `buf`
	themeStylesheet, err := themeCSS(themeID, darkThemeID)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(upperBuf, `<style type="text/css"><![CDATA[%s%s]]></style>`, baseStylesheet, themeStylesheet)

	hasMarkdown := false
	for _, s := range diagram.Shapes {
		if s.Label != "" && s.Type == d2target.ShapeText {
			hasMarkdown = true
			break
		}
	}
	if hasMarkdown {
		fmt.Fprintf(upperBuf, `<style type="text/css">%s</style>`, mdCSS)
	}
	if sketchRunner != nil {
		d2sketch.DefineFillPatterns(upperBuf)
	}

	// This shift is for background el to envelop the diagram
	left -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
	top -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
	w += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)
	h += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)
	backgroundEl := d2themes.NewThemableElement("rect")
	// We don't want to change the document viewbox, only the background el
	backgroundEl.X = float64(left)
	backgroundEl.Y = float64(top)
	backgroundEl.Width = float64(w)
	backgroundEl.Height = float64(h)
	backgroundEl.Fill = diagram.Root.Fill
	backgroundEl.Stroke = diagram.Root.Stroke
	backgroundEl.Rx = float64(diagram.Root.BorderRadius)
	if diagram.Root.StrokeDash != 0 {
		dashSize, gapSize := svg.GetStrokeDashAttributes(float64(diagram.Root.StrokeWidth), diagram.Root.StrokeDash)
		backgroundEl.StrokeDashArray = fmt.Sprintf("%f, %f", dashSize, gapSize)
	}
	backgroundEl.Attributes = fmt.Sprintf(`stroke-width="%d"`, diagram.Root.StrokeWidth)

	// This shift is for viewbox to envelop the background el
	left -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
	top -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
	w += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)
	h += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)

	doubleBorderElStr := ""
	if diagram.Root.DoubleBorder {
		offset := d2target.INNER_BORDER_OFFSET

		left -= int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.)) + offset
		top -= int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.)) + offset
		w += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.)*2.) + 2*offset
		h += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.)*2.) + 2*offset

		backgroundEl2 := backgroundEl.Copy()
		// No need to double-paint
		backgroundEl.Fill = "transparent"

		backgroundEl2.X = float64(left)
		backgroundEl2.Y = float64(top)
		backgroundEl2.Width = float64(w)
		backgroundEl2.Height = float64(h)
		doubleBorderElStr = backgroundEl2.Render()

		left -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
		top -= int(math.Ceil(float64(diagram.Root.StrokeWidth) / 2.))
		w += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)
		h += int(math.Ceil(float64(diagram.Root.StrokeWidth)/2.) * 2.)
	}

	var dimensions string
	if setDimensions {
		dimensions = fmt.Sprintf(` width="%d" height="%d"`, w, h)
	}

	fitToScreenWrapper := fmt.Sprintf(`<svg %s preserveAspectRatio="xMinYMin meet" viewBox="0 0 %d %d"%s>`,
		`xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"`,
		w, h,
		dimensions,
	)

	// TODO minify
	docRendered := fmt.Sprintf(`%s%s<svg id="d2-svg" width="%d" height="%d" viewBox="%d %d %d %d">%s%s%s%s</svg></svg>`,
		`<?xml version="1.0" encoding="utf-8"?>`,
		fitToScreenWrapper,
		w, h, left, top, w, h,
		doubleBorderElStr,
		backgroundEl.Render(),
		upperBuf.String(),
		buf.String(),
	)
	return []byte(docRendered), nil
}

// TODO include only colors that are being used to reduce size
func themeCSS(themeID int64, darkThemeID *int64) (stylesheet string, err error) {
	out, err := singleThemeRulesets(themeID)
	if err != nil {
		return "", err
	}

	if darkThemeID != nil {
		darkOut, err := singleThemeRulesets(*darkThemeID)
		if err != nil {
			return "", err
		}
		out += fmt.Sprintf("@media screen and (prefers-color-scheme:dark){%s}", darkOut)
	}

	return out, nil
}

func singleThemeRulesets(themeID int64) (rulesets string, err error) {
	out := ""
	theme := d2themescatalog.Find(themeID)

	// Global theme colors
	for _, property := range []string{"fill", "stroke", "background-color", "color"} {
		out += fmt.Sprintf(".%s-N1{%s:%s;}.%s-N2{%s:%s;}.%s-N3{%s:%s;}.%s-N4{%s:%s;}.%s-N5{%s:%s;}.%s-N6{%s:%s;}.%s-N7{%s:%s;}.%s-B1{%s:%s;}.%s-B2{%s:%s;}.%s-B3{%s:%s;}.%s-B4{%s:%s;}.%s-B5{%s:%s;}.%s-B6{%s:%s;}.%s-AA2{%s:%s;}.%s-AA4{%s:%s;}.%s-AA5{%s:%s;}.%s-AB4{%s:%s;}.%s-AB5{%s:%s;}",
			property, property, theme.Colors.Neutrals.N1,
			property, property, theme.Colors.Neutrals.N2,
			property, property, theme.Colors.Neutrals.N3,
			property, property, theme.Colors.Neutrals.N4,
			property, property, theme.Colors.Neutrals.N5,
			property, property, theme.Colors.Neutrals.N6,
			property, property, theme.Colors.Neutrals.N7,
			property, property, theme.Colors.B1,
			property, property, theme.Colors.B2,
			property, property, theme.Colors.B3,
			property, property, theme.Colors.B4,
			property, property, theme.Colors.B5,
			property, property, theme.Colors.B6,
			property, property, theme.Colors.AA2,
			property, property, theme.Colors.AA4,
			property, property, theme.Colors.AA5,
			property, property, theme.Colors.AB4,
			property, property, theme.Colors.AB5,
		)
	}

	// Appendix
	out += fmt.Sprintf(".appendix text.text{fill:%s}", theme.Colors.Neutrals.N1)

	// Markdown specific rulesets
	out += fmt.Sprintf(".md{--color-fg-default:%s;--color-fg-muted:%s;--color-fg-subtle:%s;--color-canvas-default:%s;--color-canvas-subtle:%s;--color-border-default:%s;--color-border-muted:%s;--color-neutral-muted:%s;--color-accent-fg:%s;--color-accent-emphasis:%s;--color-attention-subtle:%s;--color-danger-fg:%s;}",
		theme.Colors.Neutrals.N1, theme.Colors.Neutrals.N2, theme.Colors.Neutrals.N3,
		theme.Colors.Neutrals.N7, theme.Colors.Neutrals.N6,
		theme.Colors.B1, theme.Colors.B2,
		theme.Colors.Neutrals.N6,
		theme.Colors.B2, theme.Colors.B2,
		theme.Colors.Neutrals.N2, // TODO or N3 --color-attention-subtle
		"red",
	)

	// Sketch style specific rulesets
	// B
	lc, err := color.LuminanceCategory(theme.Colors.B1)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B1, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.B2)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B2, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.B3)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B3, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.B4)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B4, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.B5)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B5, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.B6)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.B6, lc, blendMode(lc))

	// AA
	lc, err = color.LuminanceCategory(theme.Colors.AA2)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.AA2, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.AA4)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.AA4, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.AA5)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.AA5, lc, blendMode(lc))

	// AB
	lc, err = color.LuminanceCategory(theme.Colors.AB4)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.AB4, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.AB5)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.AB5, lc, blendMode(lc))

	// Neutrals
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N1)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N1, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N2)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N2, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N3)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N3, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N4)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N4, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N5)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N5, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N6)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N6, lc, blendMode(lc))
	lc, err = color.LuminanceCategory(theme.Colors.Neutrals.N7)
	if err != nil {
		return "", err
	}
	out += fmt.Sprintf(".sketch-overlay-%s{fill:url(#streaks-%s);mix-blend-mode:%s}", color.N7, lc, blendMode(lc))

	if theme.IsDark() {
		out += fmt.Sprintf(".light-code{display: none}")
		out += fmt.Sprintf(".dark-code{display: block}")
	} else {
		out += fmt.Sprintf(".light-code{display: block}")
		out += fmt.Sprintf(".dark-code{display: none}")
	}

	return out, nil
}

func blendMode(lc string) string {
	switch lc {
	case "bright":
		return "darken"
	case "normal":
		return "color-burn"
	case "dark":
		return "overlay"
	case "darker":
		return "lighten"
	}
	panic("invalid luminance category")
}

type DiagramObject interface {
	GetID() string
	GetZIndex() int
}

// sortObjects sorts all diagrams objects (shapes and connections) in the desired drawing order
// the sorting criteria is:
// 1. zIndex, lower comes first
// 2. two shapes with the same zIndex are sorted by their level (container nesting), containers come first
// 3. two shapes with the same zIndex and same level, are sorted in the order they were exported
// 4. shape and edge, shapes come first
func sortObjects(allObjects []DiagramObject) {
	sort.SliceStable(allObjects, func(i, j int) bool {
		// first sort by zIndex
		iZIndex := allObjects[i].GetZIndex()
		jZIndex := allObjects[j].GetZIndex()
		if iZIndex != jZIndex {
			return iZIndex < jZIndex
		}

		// then, if both are shapes, parents come before their children
		iShape, iIsShape := allObjects[i].(d2target.Shape)
		jShape, jIsShape := allObjects[j].(d2target.Shape)
		if iIsShape && jIsShape {
			return iShape.Level < jShape.Level
		}

		// then, shapes come before connections
		_, jIsConnection := allObjects[j].(d2target.Connection)
		return iIsShape && jIsConnection
	})
}

func hash(s string) string {
	const secret = "lalalas"
	h := fnv.New32a()
	h.Write([]byte(fmt.Sprintf("%s%s", s, secret)))
	return fmt.Sprint(h.Sum32())
}

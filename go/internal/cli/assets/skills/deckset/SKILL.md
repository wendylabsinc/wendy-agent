---
name: deckset
description: 'Example for using Deckset to create Markdown presentations'
---
# Headings

```markdown
# Large Heading
```
![Large Heading]({{ page.rel_root }}/img/slide-04.md.png)

```markdown
## Regular Heading
```
![Regular Heading]({{ page.rel_root }}/img/slide-05.md.png)

```markdown
### Small Heading
```
![Small Heading]({{ page.rel_root }}/img/slide-06.md.png)

```markdown
#### Tiny Heading
```
![Tiny Heading]({{ page.rel_root }}/img/slide-07.md.png)

```markdown
## Combine Headings

### Of Different Sizes
```
![Combine Headings]({{ page.rel_root }}/img/slide-08.md.png)

```markdown
# [fit] Make Headings Fit Onto

# [fit] The Slide
```
![Fit Headings]({{ page.rel_root }}/img/slide-09.md.png)


# Lists

```markdown
# Unordered Lists

- Start each bullet point
- with a dash to create
- an unordered list
```
![Unordered Lists]({{ page.rel_root }}/img/slide-10.md.png)

```markdown
# Ordered Lists

1. Start each item with
1. a number followed by a dot
1. to create an ordered list
```
![Ordered Lists]({{ page.rel_root }}/img/slide-11.md.png)

```markdown
# Use Source List Numbering

[.use-source-list-numbering]

1. Start at one
3. Skip a number
5. Keep going
```
This command applies to the next ordered list on the slide.

```markdown
# Nested Lists

- You can create nested lists
    1. by indenting
    1. each item with
    1. 4 spaces
- It's that simple
```
![Nested Lists]({{ page.rel_root }}/img/slide-12.md.png)


# Text Styles

```markdown
# Asterisk Emphasis

Use single asterisks around text to *emphasize it*.

Or use double asterisks for a **strong emphasis** style.
```
![Asterisk Emphasis]({{ page.rel_root }}/img/slide-13.md.png)

```markdown
# Underscore Emphasis

Alternatively, you can also use underscores to emphasize:

Wrap text in single underscores to _emphasize it_. Or use double underscores for the alternative __strong emphasis style__.
```
![Underscore Emphasis]({{ page.rel_root }}/img/slide-14.md.png)

```markdown
# Combined Emphasis

Combining underscores with asterisks lets us mix and match the emphasis styles. Play with it — some themes have additional style options for those combinations:

- _**Style 1**_
- __*Style 2*__
- __**Style 3**__
```
![Combined Emphasis]({{ page.rel_root }}/img/slide-15.md.png)

```markdown
# The same *styles* work in **headings**, too.
```
![Styling Headings]({{ page.rel_root }}/img/slide-16.md.png)

```markdown
# More Styles

- ~~Strikethrough~~
- Superscript
- Subscript
- `Inline code`
```
![More Styles]({{ page.rel_root }}/img/slide-17.md.png)


# Quotes

```markdown
> The best way to predict the future is to invent it
-- Alan Kay
```
![Block Quotes]({{ page.rel_root }}/img/slide-18.md.png)

```markdown
# Inline Quotes

You can also use a quote together with paragraph text or other elements on the slide:

> The best way to predict the future is to invent it
-- Alan Kay

Prefix the author of the quote with `--`, or leave it out if it's anonymous.
```
![Inline Quotes]({{ page.rel_root }}/img/slide-19.md.png)


# Links

```markdown
# Link to External Resources

In case you're looking for something, you could use [Google](http://google.com) or [Wikipedia](http://wikipedia.com).

Links will be clickable in exported PDFs as well!
```
![Link to External Resources]({{ page.rel_root }}/img/slide-44.md.png)

```markdown
# Links Between Slides

Define an anchor on the slide you want to link to using standard HTML syntax:

``

Then you can link to this [slide](#link-target) easily.
```
![Link Between Slides]({{ page.rel_root }}/img/slide-45.md.png)


# Code Blocks

[//]: # "TODO: Support back ticks for code blocks within markdown code blocks"


## Syntax Highlighting

Use GitHub style fenced code blocks to specify the language.

```javascript
$.ajax({
  url: "/api/getWeather",
  data: {
    zipcode: 97201
  },
  success: function( data ) {
    $( "#weather-temp" ).html( "" + data + " degrees" );
  }
});
```


![Syntax Highlighting]({{ page.rel_root }}/img/slide-38.md.png)

## Highlight Lines of Code

To put the focus on specific lines of your code block, use the following command:

[.code-highlight: 2]

```javascript
$.ajax({
  url: "/api/getWeather",
  data: {
    zipcode: 97201
  },
  success: function( data ) {
    $( "#weather-temp" ).html( "" + data + " degrees" );
  }
});
```


![Highlight line of code]({{ page.rel_root }}/img/highlight-single-loc.png)

You can also highlight a range of lines:

[.code-highlight: 2, 6-8]

```javascript
$.ajax({
  url: "/api/getWeather",
  data: {
    zipcode: 97201
  },
  success: function( data ) {
    $( "#weather-temp" ).html( "" + data + " degrees" );
  }
});
```


![Highlight ranges]({{ page.rel_root }}/img/highlight-range-loc.png)

## Step through Highlighted Lines of Code

When presenting, you can step through multiple highlights incrementally. Place as many `[.code-highlight]` commands above a code block in the order you would like the lines of code to be highlighted when presenting.

[.code-highlight: none]
[.code-highlight: 2]
[.code-highlight: 6-8]
[.code-highlight: all]

```javascript
$.ajax({
  url: "/api/getWeather",
  data: {
    zipcode: 97201
  },
  success: function( data ) {
    $( "#weather-temp" ).html( "" + data + " degrees" );
  }
});
```


## Automatic Scaling

Don’t worry if your code is slightly too long. Deckset scales code blocks to fit automatically.

```ruby
def establish_connection(spec = nil)
  spec     ||= DEFAULT_ENV.call.to_sym
  resolver =   ConnectionAdapters::ConnectionSpecification::Resolver.new configurations
  spec     =   resolver.spec(spec)

  unless respond_to?(spec.adapter_method)
    raise AdapterNotFound, "database configuration specifies nonexistent #{spec.config[:adapter]} adapter"
  end

  remove_connection
  connection_handler.establish_connection self, spec
end
```


![Automatic Scaling]({{ page.rel_root }}/img/slide-39.md.png)

## Inline `code`

Use code within normal text by enclosing it in backticks like this:

`func map(x: A?, f: A -> B) -> B?`


![Inline Code]({{ page.rel_root }}/img/slide-40.md.png)

## Code background

Use Deckset’s customization panel for your current theme to set a custom code background:

![Inline Code]({{ page.rel_root }}/img/code-background.png)

This setting isn’t exposed via Markdown commands yet, so let us know if making it scriptable would help your workflow.


# Tables

Cells are separated by pipes `|`.

Table headers are separated from the table body with a line of three dashes `---`.

```markdown
| Header 1 | Header 2 | Header 3 |
| --- | --- | --- |
| Cell 1 | Cell 2 | Cell 3 |
| Cell 4 | Cell 5 | Cell 6 |
```

![Tables]({{ page.rel_root }}/img/tables.png)

## Text Alignment

With `:---:` and `---:` you can center or right align the cell content.

```markdown
|   Header 1  |    Header 2   |   Header 3   |
| ----------- | :-----------: | -----------: |
| Cell        |    _Cell_     |    *Cell*    |
| Cell        |   **Cell**    |   __Cell__   |
```

![Tables]({{ page.rel_root }}/img/tables-text-alignment.png)


# Formulas

Easily include mathematical formulas by enclosing TeX commands in `$$` delimiters. Deckset uses [MathJax](http://www.mathjax.org/) to translate TeX commands into beautiful vector graphics.

```markdown
$$
\left( \sum_{k=1}^n a_k b_k \right)^2 \leq \left( \sum_{k=1}^n a_k^2 \right) \left( \sum_{k=1}^n b_k^2 \right)
$$
```
![Formulas]({{ page.rel_root }}/img/formula-basics.png)

## Inline Formulas

You can also include Formulas in paragraph text. Deckset takes care of adjusting the font size and color to match the surrounding text, for example:

```markdown
The slope $$a$$ of the line defined by the function $$f(x) = 2x$$ is $$a = 2$$.
```
![Inline Formulas]({{ page.rel_root }}/img/formula-inline.png)

## Formula Autoscaling

Don't worry if your equations get really complex. Deckset will scale them down to fit onto the slide.

```markdown
$$
1 +  \frac{q^2}{(1-q)}+\frac{q^6}{(1-q)(1-q^2)}+\cdots =
\prod_{j=0}^{\infty}\frac{1}{(1-q^{5j+2})(1-q^{5j+3})},
\quad\quad \text{for $|q|<1$}.
$$
```
![Formula Autoscaling]({{ page.rel_root }}/img/formula-autofit.png)


# Emojis

Deckset supports Slack-style emojis, e.g.: `:sunny: :umbrella: :sunflower: :cat: :smile:`

```markdown
Deckset supports emojis! :umbrella: :sunflower: :cat: :smile: :thumbsup:
```
![Emojis]({{ page.rel_root }}/img/emojis.png)

Please refer to this [Emoji Reference Markdown file](http://deckset-assets.s3.amazonaws.com/emoji-reference.md) to see which emoji are supported in Deckset. There are a few favorites that aren’t part of Unicode yet, so we cannot support them.


# Footers and Slide Numbers

To add a persistent footer to each slide of your presentation, insert the following command at the *top* of your file:

`footer: Your footer goes here`

To add running slide numbers to each slide of your presentation, insert the following command at the *top* of your file:

`slidenumbers: true`

When combining the two commands, please make sure that there are *no empty lines* between the two.
```markdown
footer: Your footer goes here
slidenumbers: true
```

You can use standard text styles such as emphasis in your footer text, just as you would in other places too.

`footer: Use *emphasis* and ~~other~~ text styles if you like`

For richer footers, use the multi-line block form and include full markdown, images, or columns:

```markdown
footer: <<<
[.column]
Left footer text

[.column]
Right footer text

[.end-columns]
>>>
```

```markdown
footer: <<<
![Logo](logo.png)
>>>
```

If you want to disable footers or slide numbers on individual slides, you can do so by using [per slide commands](../Customization/01-configuration-commands.html).


# Footnotes

```markdown
# Footnotes

Footnotes are a breeze, for example:

Most of the time, you should adhere to the APA citation style[^1].

Note that footnote references have to be *unique in the Markdown file*. This means you can reference footnotes from any slide, no matter where they are defined.

[^1]: For more details on the citation guidelines of the American Psychological Association check out their [website](https://www.library.cornell.edu/research/citation/apa).
```
![Footnotes]({{ page.rel_root }}/img/slide-46.md.png)

```markdown
# Named References

Instead of just numbers, you can also name your footnote references[^Wiles, 1995].

[^Wiles, 1995]: [Modular elliptic curves and Fermat's Last Theorem](http://math.stanford.edu/~lekheng/flt/wiles.pdf). Annals of Mathematics 141 (3): 443–551.
```
![Named References]({{ page.rel_root }}/img/slide-47.md.png)


# Controlling Line Breaks

```markdown
# Controlling Line Breaks

In paragraph text, Deckset respects when you start a
new
line.

This can come in handy in situations where you need more control over how text is broken up into multiple lines.
```
![Controlling Line Breaks]({{ page.rel_root }}/img/slide-52.md.png)

```markdown
# Use `
` for
line
breaks

You can use the HTML tag `
` to insert line breaks in elements that cannot contain regular new lines, such as headings or footers.
```
![Use break tag for line breaks]({{ page.rel_root }}/img/slide-53.md.png)


# Auto-Scaling

```markdown
# Auto-Scaling

At times you have to fit more content onto one slide than the default font sizes allow for.

Deckset comes with an option to auto-scale paragraph text, lists, and other body content down to fit onto the slide. To enable this behavior put

`autoscale: true`

on the first line of your markdown file.
```
![Auto-Scaling]({{ page.rel_root }}/img/slide-54.md.png)


# Columns

To lay out your content in columns, use the `[.column]` command to start a new column.


```markdown

[.column]

# First column

[.column]

# Second column.

```

Column widths are automatically generated using the available space divided evenly by the number of columns specified.

You can add as many columns as you like, but the more you add, the smaller the available width will be.

Combine columns with [Auto-Scaling](https://docs.deckset.com/English.lproj/Formatting/13-auto-scaling.html) to automatically scale down the text size so it fits on the slide.


# Background Images

## Full Background Image
```markdown
![](image1.jpg)
```
![Full Background Image]({{ page.rel_root }}/img/slide-20.md.png)

## Fit Background Image
```markdown
![fit](image1.jpg)
```
![Fit Background Image]({{ page.rel_root }}/img/slide-21.md.png)

## Multiple Background Images
```markdown
![](image1.jpg)
![](image2.jpg)
![](image3.jpg)
```
![Multiple Background Images]({{ page.rel_root }}/img/slide-22.md.png)

## Text on Images
```markdown
![](image2.jpg)

# Text on Images

Setting text on images applies a filter to the image to make the text more readable.
```
![Text on Images]({{ page.rel_root }}/img/slide-23.md.png)

## Disable Filter
```markdown
![original](image2.jpg)

# Disable Filter
```
![Disable Filter]({{ page.rel_root }}/img/slide-24.md.png)

## Zoom In
```markdown
![original 250%](image2.jpg)

# Zoom In
```
![Zoom In]({{ page.rel_root }}/img/slide-25.md.png)

## Split Slides
```markdown
![left](image1.jpg)

# Split Slides

Use the `left` or `right` modifiers to place the image in the left or right half of the slide, respectively.
```
![Split Slides]({{ page.rel_root }}/img/slide-26.md.png)

## Adjust Filters
```markdown
![left filtered](image1.jpg)

# Force Apply Filter

Use the `filtered` keyword to apply the theme's filter to an image that isn't filtered by default.
```
![Force Apply Filter]({{ page.rel_root }}/img/slide-27.md.png)

## Split Slides with Fit
```markdown
![right fit](image2.jpg)

# Split Slides

Combine `left` or `right` with the `fit` keyword or a percentage to adjust the image scaling.
```
![Split Slides Fit]({{ page.rel_root }}/img/slide-28.md.png)

## Alpha

Use `alpha` on an image to specify its opacity.

```markdown
![right alpha(0.6)](image2.jpg)
```

## Global Background Image

To apply a background image to every slide in your presentation, simply add the `background-image` command at the very top of your Markdown file:

```markdown
background-image: image2.jpg

(rest of your content)
```


# Inline Images

## Combine Text and Images
```markdown
![inline](image1.jpg)
```
![Combine Text and Images]({{ page.rel_root }}/img/slide-29.md.png)

## Fill the Slide

```markdown
![inline fill](image1.jpg)
```
![Fill the Slide]({{ page.rel_root }}/img/slide-30.md.png)

## Custom Scaling

```markdown
![inline 50%](image1.jpg)
```
![Custom Scaling]({{ page.rel_root }}/img/slide-31.md.png)

## Image Grids

```markdown
![inline fill](image1.jpg)![inline fill](image2.jpg)
![inline fill](image3.jpg)
```
![Image Grids]({{ page.rel_root }}/img/slide-32.md.png)

## Corner Radius

![Rounded Corners]({{ page.rel_root }}/img/rounded-corners.jpeg)

You can set corner radius of images using following Markdown:

```markdown
![inline corner-radius(16)](image1.jpg)
```

This currently only works for plain static images. Let us know if you'd like it to work for other media too.


# Videos

```markdown
![](video.mov)
```
![Videos]({{ page.rel_root }}/img/slide-33.md.png)

```markdown
# Inline Videos

![inline](video.mov)
```
![Inline Videos]({{ page.rel_root }}/img/slide-34.md.png)

```markdown
# YouTube Embeds!

![](https://www.youtube.com/watch?v=ekz6i58kVpI)

You can also use URL parameters like `?t=30s` to specify a start time for the clip.
```
![YouTube Embeds]({{ page.rel_root }}/img/slide-35.md.png)

```markdown
![left](video.mov)

# Video Layout Control

Use the same layout modifiers as with images to control the positioning of videos.

- `left` and `right`
- `fit` and `fill`
- Percentage sizing, e.g. `50%`
- `hide` to hide the video. Audio will play regardless.

```
![Video Layout Control]({{ page.rel_root }}/img/slide-36.md.png)

```markdown
![right autoplay mute](video.mov)
```

## Video Playback Control

Add playback directives after the filename to tweak how the clip behaves:

- `autoplay` — start the video automatically when the slide appears.
- `loop` — replay the video continuously.
- `mute` — silence the audio track.

Mix and match them as needed:

```markdown
![right autoplay mute](video.mov)
```

![Video Playback Control]({{ page.rel_root }}/img/slide-37.md.png)

## Automatic slide advancement

You can add `autoadvance` to the modifiers to jump to the next slide as soon as the video finishes:

```markdown
![autoadvance](video.mov)
```

This only works for the last video on the slide and the slide cannot contain build lists.


# Audio

To add audio files to your presentation, add them to your Markdown like so:

```markdown
![](audio.mp3)
```

Your operating system’s file type icon will be used as a visual representation of the audio file.

## Audio Layout Control

Use the same layout modifiers as with images to control the positioning of audio file icons.

- `left` and `right`
- `fit` and `fill`
- Percentage sizing, e.g. `25%`
- `hide` to hide the visual representation. Audio will play regardless.

## Audio Playback Control

Control audio playback by using one of those directives:

- `autoplay`
- `loop`
- `mute`


# Markdown Graphs

You can add graphs to your presentation by leveraging code blocks and specifying `mermaid` as the language:

```mermaid
pie showData
    title Key elements in Product X
    "Calcium" : 42.96
    "Potassium" : 50.05
    "Magnesium" : 10.01
    "Iron" :  5
```


You can also use the customization menu to specify base colors for Mermaid graphs:

![Inline Code]({{ page.rel_root }}/img/mermaid-customization.png)

You can override Mermaid colors per slide with the markdown graph customization command. Provide up to three colors plus an optional dark-mode flag:

```markdown
[.graph: #ffffff, #aaffff, #000000, dark-mode(false)]
```

See also [Custom theming](../Customization/02-custom-theming.html) for more slide-level overrides.

Learn more about available graph types in the [Mermaid.js documentation](https://mermaid.js.org/intro/). Zenuml diagrams are not supported yet—let us know via support if you need them.


# Pikchr Diagrams

Pikchr diagrams work the same way as other inline media—wrap your markup in a fenced code block and declare the language as `pikchr`:

```pikchr
arrow right 200% "Markdown" "Source"
box rad 10px "Markdown" "Formatter" "(markdown.c)" fit
arrow right 200% "HTML+SVG" "Output"
arrow <-> down 70% from last box.s
box same "Pikchr" "Formatter" "(pikchr.c)" fit
```


You can reuse the graph customization command to control the base colors that Pikchr uses. This lets the diagram blend in with your slide theme:

![Pikchr Diagram Customization]({{ page.rel_root }}/img/pikchr-diagram-customization.png)

You can also override the Pikchr colors per slide using the markdown command. The first color controls strokes and text; the second is the diagram background:

```markdown
[.pikchr-graph: #f8f8f8, #003366]
```

See also [Custom theming](../Customization/02-custom-theming.html) for more slide-level overrides.

Need more syntax ideas? Browse the [official Pikchr documentation](https://pikchr.org/home/doc/trunk/doc/examples.md) for ready‑made examples you can paste into Deckset.


# Configuration commands

Use commands to configure your presentation settings. Commands can either be applied *globally* — if put at the top of your Markdown file — or *per slide* to set or override global settings. When using global configuration commands, make sure that there are *no line breaks* between any other commands at the top of your markdown file.

## Scale content to fit the slide

At times you have to fit more content onto one slide than the default font sizes allow for.

Deckset comes with an option to auto-scale paragraph text, lists, and other body content down to fit onto the slide. To enable this behavior put the following command at the top of your markdown file:

```markdown
autoscale: true
```

You may turn this on or off on an individual slide by declaring `[.autoscale: true]` or `[.autoscale: false]` on the respective slide.

## Build lists

In Presenter and Rehearsal mode, you may want list items to appear one by one. To achieve this with Deckset, use the following command: 

```markdown
build-lists: true
```

You can also specify different modes of building lists:

```markdown
build-lists: all # Equivalent to true
build-lists: notFirst # Will always show first item of a list
```

Please note that this only affects Presenter (and Rehearsal) mode.

You may turn this on or off on an individual slide by declaring `[.build-lists: true]` or `[.build-lists: false]` on the respective slide.

## Slide numbers

To add running slide numbers to your presentation use the following command:

```markdown
slidenumbers: true
```

You may turn this on or off on an individual slide by declaring `[.slidenumbers: true]` or `[.slidenumbers: false]` on the respective slide.

To add the count of slides in your presentation, use the `slidecount: true` command. You may also turn this off on an individual slide by declaring `[.slidecount: true]` or `[.slidecount: false]` on the respective slide.

## Footers

To add a persistent footer to each slide of your presentation, use the following command:

```markdown
footer: My Footer content
```

To override your global footer or to add a footer to a single slide, add `[.footer: A different footer]` to the respective slide.

You may turn off the footer on an individual slide by declaring `[.hide-footer]` on the respective slide.

For multi-line footers or richer layouts, use the block form and include full markdown, images, or columns:

```markdown
footer: <<<
[.column]
Left footer text

[.column]
Right footer text

[.end-columns]
>>>
```

```markdown
footer: <<<
![Logo](logo.png)
>>>
```

## Slide transitions

In Presenter mode, you may want to transition between slides. To achieve this with Deckset, use the following command:

```markdown
slide-transition: true
```

You can control the duration of the transition by passing in the value in seconds: `slide-transition: fade(0.3)`.

You may also use this command on a single slide, like so: `[.slide-transition: fade(0.3)]` or `[.slide-transition: false]`.

## Use headers as slide dividers

To automatically start a new slide with each header, use the 

```markdown
slide-dividers: #, ##, ###, ####
```

command and specify the header levels you would like to take into account.

Please note that this command can only be used globally, at the top of your Markdown file.

## Treat paragraphs as presenter notes

To move regular paragraphs into presenter notes (without having to prefix them with `^`), use:

```markdown
paragraphs-as-presenter-notes: true
```

Please note that this command can only be used globally, at the top of your Markdown file.

## Time budget

To add a presentation time budget (in minutes) to Presenter Notes, use:

```markdown
time-budget: 20
```

The budget timer starts when you advance from slide 1 to slide 2. If you jump to a later slide, the timer starts immediately with time credited based on the average time per slide. The timer stays green while you are on pace, turns yellow when you fall behind pace, and turns red after the total budget is exceeded.

Please note that this command can only be used globally, at the top of your Markdown file.

## Default code language

You can specify a default code language by adding it at the top of your Markdown file:

```markdown
code-language: Swift
```

## Store theme choice in document

Deckset does store the theme choice as metadata to the file. In some cases, this information can get lost, e.g. in version control systems or when sending the file via email.

To make sure the theme choice doesn’t get lost, you can use the following command, referencing any theme by its name: `theme: Fira`.

If you also want to specify the color choice, you can reference the number of the color swatch as it appears within Deckset e.g.:

```markdown
theme: Fira, 3
```

Please note that this command can only be used globally, at the top of your Markdown file.

## Automatically fit headers

Deckset allows you to fit header to entire slide to get that signature font look. However repeating this for every slide might be a bit cumbersome. You can address that by using the `fit-header` command like so:

```markdown
fit-header: #, ##
```

Note that this only works as a global command and needs to be placed on top of your md file.

## Global Background Image

To apply a background image to every slide in your presentation, simply add the `background-image` command at the very top of your Markdown file:

```markdown
background-image: image2.jpg

(rest of your content)
```

## Default image corner radius

To set a global default corner radius for images, add the following command at the top of your Markdown file:

```markdown
image-corner-radius: 12
```

This default applies only when no explicit `corner-radius(...)` is provided in the image alt text. For example:

```markdown
![inline corner-radius(24)](team.jpg)
```


# Custom theming

Deckset 2.0 introduces customization options for slide styles directly within the user interface. In addition to these UI-based controls, you can apply fine-grained styling to individual slides using the customization commands outlined below. Note that command-based styles will always override any styles set in the UI.

## Text and Headers

To customize the look and feel of your text and headers, you may add customization commands to `text`, `text-emphasis`, `text-strong`, `header`, `header-emphasis`, `header-strong`, `slidenumber-style` and `footer-style` with the desired color, alignment, line-height, text-scale, font and capitalization.

```markdown
[.text: #000000, alignment(left|center|right), line-height(10), text-scale(2.0), kern(1), Avenir Next Regular]
[.header: #FF0000, alignment(left|center|right), line-height(18), text-scale(3.0), Avenir Next Bold, capitalization(default|upper|title)]
[.footer-style: #2F2F2F, alignment(left|center|right), line-height(8), text-scale(1.5), Avenir Next Regular]
```

We recommend using Mac's native Font Book application to search for fonts.

## Background color

To customize the background color of your slide, you may use the following customization command:

```markdown
[.background-color: #FF0000]
```

## Lists

To customize your list styles, you may add customization commands for the list color, bullet-character, alignment, bullet color, text scale, and ordered and unordered bullet indent.

```markdown
[.list: #000000, bullet-character(Custom String), alignment(left|center|right), bullet-color(#ffcc00), text-scale(1.2), bullet-indent(40), ordered-bullet-indent(20)]
```

## Tables

To customize the style of your tables, you may use the following commands:

```markdown
[.table-separator: #000000, stroke-width(10)] 
[.table: margin(5)]
```

## Code

Deckset automatically picks a random set of colors with high enough contrast against the chosen background color for your code. You may add customization commands for the line-height and font family.

For consistent styles between code blocks and to prevent palette regeneration, use seed number `42` for persistence.

```markdown
[.code: auto(42), Font Family Name, line-height(4.2)]
```

Use `auto` without a seed to regenerate colors on each display:

```markdown
[.code: auto]
```

Manual palettes support 1–5 colors. If fewer than five are provided, the list repeats to fill the palette.

```markdown
[.code: #ffcc00, #1b1b1b, #ffffff]
```

You can also control alpha code build-in by specifying following command:

```markdown
[.code: dimmed-opacity(0.2)]
```

Standard text overrides such as `text-scale(...)` and `alignment(...)` also apply to code blocks.

## Inline code

Inline code shares the same customization options as code blocks, but only affects inline code spans. If both `.code` and `.inline-code` are present, inline code uses the `.inline-code` override.

```markdown
[.inline-code: text-scale(0.9), Font Family Name, auto(42)]
```

## Footnotes

To customize the look and feel of footnotes, you may use the following commands:

```markdown
[.footnote: #000000, alignment(left|center|right), line-height(10), text-scale(2.0), Avenir Next Regular]
```

Style the horizontal ruler that separates the footnote from the slide content like so:

```markdown
[.footnote-separator: #001100, height(10), margin(12)]
```

## Formulas

To customize the style of formulas, you may use the following command:

```markdown
[.formula: text-scale(42), alignment(center), #ff0011]
```

## Links

Link styles can be customized with color, font, line height, text scale, and underline options. The underline style supports `none`, `single`, `thick`, or `double`, with an optional `byWord` modifier.

```markdown
[.link: #00aaff, Avenir Next Regular, line-height(10), text-scale(1.1), underline(single, byWord)]
```

## Quotes

To customize the style of quotes, you may use the following commands:

```markdown
[.quote: #000000, alignment(left|center|right), line-height(10), text-scale(2.0), Avenir Next Regular]
```

To customize the style of the quote’s author, use:

```markdown
[.quote-author: #000000, alignment(left|center|right), line-height(10), text-scale(2.0), Avenir Next Regular]
```

## Presenter notes

Presenter notes accept the same text customization controls as normal text styles.

```markdown
[.presenter-notes: #333333, alignment(left), line-height(9), text-scale(0.9), Avenir Next Regular]
```

See also [Presenter notes](../Presenting/presenter-notes.html) for authoring and preview tips.

## Mermaid graphs

Mermaid diagrams use the shared graph customization command so you can pick distinct primary, secondary, and tertiary colors (plus an optional dark‑mode toggle):

```markdown
[.graph: #ffffff, #aaffff, #000000, dark-mode(false)]
```

See also [Mermaid diagrams](../Media/05-mermaid.html) for syntax and examples.

## Pikchr diagrams

Pikchr diagrams have their own two-color command. The first value controls the diagram foreground (strokes, text, etc.) and the second value is the background fill; omit the second color to fall back to the slide background:

```markdown
[.pikchr-graph: #f8f8f8, #003366]
```

See also [Pikchr diagrams](../Media/06-pikchr.html) for syntax examples.

## Store theme choice in document

Deckset does store the theme choice as metadata to the file. In some cases, this information can get lost, e.g. in version control systems or when sending the file via email.

To make sure the theme choice doesn’t get lost, you can use the following command `theme: Fira`.

If you also want to specify the color choice, you can reference the number of the color swatch as it appears within Deckset e.g.:

```markdown
theme: Fira, 3
```

Please note that this command can only be used globally, at the top of your Markdown file.


# Build Steps

When presenting, you often want list items to appear one by one. Enable that behavior by adding this command to the top of your file:

```markdown
build-lists: true
```

You can also specify different build modes:

```markdown
build-lists: all # Equivalent to true
build-lists: notFirst # Will always show first item of a list
```

If you want to disable or tweak this on individual slides, use [per slide commands](../Customization/01-configuration-commands.html)—for example `[.build-lists: false]` or `[.build-lists: not_first]` to always reveal the first bullet immediately and build the rest.


# Hide Slides

If you want to prevent particular slides (or pieces of content) from being presented but want to keep them in your document, you can do it like this:

```markdown

```


# Switch Presenter Display

When using a projector or secondary display, you can switch the presenter display in Presentation Mode with the `x` keyboard shortcut.

When presenting on iPad, you can advance slides by tapping on a paired Apple Pencil.


# Presenter Notes

## Using presenter notes

Deckset turns every paragraph that starts with a `^` into presenter notes and doesn’t show this text on the slides.

You’ll see these notes on the presenter display (with two screens connected) or by using the rehearsal mode.

To start another presenter note paragraph, prefix it with a caret again. Deckset will automatically scale the notes down to fit onto the presenter display in case you have a lot of text.

Example:

```markdown
# My slide title

^ This is a presenter note.
```
![Presenter Notes]({{ page.rel_root }}/img/presenter-notes.png)

## Customize the display of presenter notes

To customize the style of presenter notes, in both Presenter and Rehearsal modes and in exported documents, you may use the `presenter-notes` command like so:

```markdown
presenter-notes: #333333, alignment(left|right|center), line-height(9), text-scale(0.9), kern(0.5), capitalization(default|upper|title), Helvetica
```

You may also use this command on a single slide, like so:

```markdown
[.presenter-notes: #333333, alignment(left|right|center), line-height(9), text-scale(0.9), kern(0.5), capitalization(default|upper|title), Helvetica]
```

## Treating all paragraphs as presenter notes

You can configure Deckset to treat all paragraphs as presenter notes. To do so either add

```markdown
paragraphs-as-presenter-notes: true
```

at top of your Markdown file or turn on the "Paragraphs as presenter notes" option in presentation settings view.

## Preview presenter notes in the main window

To preview your presenter notes in the main application window, you can turn them on by selecting `Toggle Presenter Notes` from the `View` menu.

See also [Custom theming](../Customization/02-custom-theming.html) for slide-level styling options.


# Transitions

## Enabling transitions

When presenting, you may want to transition between slides. Enable transitions with this command:

```markdown
slide-transition: true
```

## Transition styles

The following transition styles are available:

* `fade` — crossfades between slides (default).
* `fadeThroughColor(#000000)` — fades through a color, into the next slide. Pass the color as a hex value in parentheses.
* `push(horizontal|vertical|top|right|bottom|left)` — the next slide pushes the previous one out of the viewport. Use `horizontal` or `vertical` (or a direction) to control the movement when advancing or going back.
* `move(horizontal|vertical|top|right|bottom|left)` — the next slide moves over the top of the current slide. Direction parameters work the same way as with `push`.
* `reveal(horizontal|vertical|top|right|bottom|left)` — the current slide moves away to reveal the next slide underneath. Direction parameters again control the direction of travel when moving forward or backward.

You can choose one of these transitions by specifying it in the command:

```markdown
slide-transition: push(vertical)
``` 

## Transition timing

You can control the duration of the transition by passing in the value in seconds:

```markdown
slide-transition: fade(0.3)
```

## Transitions on individual slides

You may also use the transition command on a single slide, for example: `[.slide-transition: push(horizontal, 0.3)]` or `[.slide-transition: false]`.

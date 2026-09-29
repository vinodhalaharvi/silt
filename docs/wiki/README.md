# docs/wiki

The GitHub wiki, kept in this repository so it is versioned with the code it
describes and reviewed in the same pull requests.

A GitHub wiki is a separate git repository. To publish:

    git clone https://github.com/vinodhalaharvi/silt.wiki.git /tmp/silt.wiki
    cp docs/wiki/*.md /tmp/silt.wiki/
    cd /tmp/silt.wiki && git add -A && git commit -m "sync from docs/wiki" && git push

File names become page names: `CI-and-downloads.md` is the page
`CI and downloads`, and links between pages use the file name without the
extension. `_Sidebar.md` is special - it renders beside every page.

These pages are examples first. Anything asserted here - a timing, an error
message, a line of output - was taken from a real run rather than written from
memory, and should be updated when it stops being true.

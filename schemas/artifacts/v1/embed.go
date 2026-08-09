package artifactschemas

import "embed"

// embeds all *.schema.json files to provide simple loading later

//go:embed *.schema.json
var Files embed.FS

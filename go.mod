module github.com/Git-Hospital/meshmedic

go 1.21

require gopkg.in/yaml.v3 v3.0.1

retract (
	[v0.3.0, v0.3.1] // published under the old path github.com/kassvl/meshmedic
	[v1.0.0-rc1, v1.0.1] // same; v1.0.1 only carries this retraction
)

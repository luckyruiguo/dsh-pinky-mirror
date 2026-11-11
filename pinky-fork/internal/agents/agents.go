// Package agents finds the AI agents on this computer and adds Pinky to
// them as an MCP server, when the person asks for it in Settings › Agents:
// with an agent's own command where it has one (claude mcp add), or by
// changing its settings file, which Pinky backs up first and changes only
// in Pinky's entry, refusing a file it cannot read. It installs Pinky's
// skill (docs/SKILL.md) where an agent reads one and keeps the copies it
// installed current (RefreshSkills), says whether each agent
// has Pinky, and takes Pinky out again.
package agents

// Server is the MCP server Pinky adds to agents: the command that starts
// it over stdio, by this Pinky's full path, and its arguments (mcp).
type Server struct {
	Command string
	Args    []string
}

// Name is the name of Pinky's entry in agents' settings.
const Name = "pinky"

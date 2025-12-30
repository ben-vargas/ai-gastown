# Federation Architecture

> Multi-workspace coordination for Gas Town and Beads

## Overview

Federation enables multiple Gas Town instances to reference each other's work,
coordinate across organizations, and track distributed projects.

## Entity Model

### Three Levels

```
Level 1: Entity    - Person or organization (flat namespace)
Level 2: Chain     - Workspace/town per entity
Level 3: Work Unit - Issues, tasks, molecules on chains
```

### URI Scheme

Full work unit reference:

```
gt://entity/chain/rig/issue-id
gt://steve@example.com/main-town/gastown/gt-xyz
```

Within a workspace, short form works:

```
gt-xyz           # Local
gastown/gt-xyz   # Different rig, same chain
```

## Relationship Types

### Employment

Track which entities belong to organizations:

```json
{
  "type": "employment",
  "entity": "alice@example.com",
  "organization": "acme.com"
}
```

### Cross-Reference

Reference work in another workspace:

```json
{
  "references": [
    {
      "type": "depends_on",
      "target": "gt://other-entity/chain/rig/issue-id"
    }
  ]
}
```

### Delegation

Distribute work across workspaces:

```json
{
  "type": "delegation",
  "parent": "gt://acme.com/projects/proj-123",
  "child": "gt://alice@example.com/town/gastown/gt-xyz",
  "terms": { "portion": "backend", "deadline": "2025-02-01" }
}
```

## Agent Provenance

Every agent operation is attributed:

### Git Commits

```bash
# Set per agent session
GIT_AUTHOR_NAME="gastown/crew/joe"
GIT_AUTHOR_EMAIL="steve@example.com"  # Workspace owner
```

Result: `abc123 Fix bug (gastown/crew/joe <steve@example.com>)`

### Beads Operations

```bash
BD_ACTOR="gastown/crew/joe"  # Set in agent environment
bd create --title="Task"     # Actor auto-populated
```

### Event Logging

All events include actor:

```json
{
  "ts": "2025-01-15T10:30:00Z",
  "type": "sling",
  "actor": "gastown/crew/joe",
  "payload": { "bead": "gt-xyz", "target": "gastown/polecats/Toast" }
}
```

## Discovery

### Workspace Metadata

Each workspace has identity metadata:

```json
// ~/gt/.town.json
{
  "owner": "steve@example.com",
  "name": "main-town",
  "public_name": "steve-gastown"
}
```

### Remote Registration

```bash
gt remote add acme gt://acme.com/engineering
gt remote list
```

### Cross-Workspace Queries

```bash
bd show gt://acme.com/eng/ac-123    # Fetch remote issue
bd list --remote=acme               # List remote issues
```

## Aggregation

Query across relationships without hierarchy:

```bash
# All work by org members
bd list --org=acme.com

# All work on a project (including delegated)
bd list --project=proj-123 --include-delegated

# Agent's full history
bd audit --actor=gastown/crew/joe
```

## Implementation Status

- [ ] Agent identity in git commits
- [ ] BD_ACTOR default in beads create
- [ ] Workspace metadata file
- [ ] Cross-workspace URI scheme
- [ ] Remote registration
- [ ] Cross-workspace queries
- [ ] Delegation primitives

## Use Cases

### Multi-Repo Projects

Track work spanning multiple repositories:

```
Project X
├── gt://team/frontend/fe-123
├── gt://team/backend/be-456
└── gt://team/infra/inf-789
```

### Distributed Teams

Team members in different workspaces:

```
Alice's Town → works on → Project X
Bob's Town   → works on → Project X
```

Each maintains their own CV/audit trail.

### Contractor Coordination

Prime contractor delegates to subcontractors:

```
Acme/Project
└── delegates to → Vendor/SubProject
                   └── delegates to → Contractor/Task
```

Completion cascades up. Attribution preserved.

## Design Principles

1. **Flat namespace** - Entities not nested, relationships connect them
2. **Relationships over hierarchy** - Graph structure, not tree
3. **Git-native** - Federation uses git mechanics (remotes, refs)
4. **Incremental** - Works standalone, gains power with federation
5. **Privacy-preserving** - Each entity controls their chain visibility

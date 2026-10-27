package protocol

// RequirementPlanOutputSchema separates a user-facing clarification from a
// candidate. The daemon submits candidate data through its task-scoped token;
// the planning model never needs filesystem or shell write permissions.
const RequirementPlanOutputSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["reply","candidate"],
  "properties":{
    "reply":{"type":"string","minLength":1},
    "candidate":{"anyOf":[{"type":"null"},{
      "type":"object","additionalProperties":false,
      "required":["goal","nonGoals","scope","constraints","acceptanceCriteria","projectId","agentId"],
      "properties":{
        "goal":{"type":"string","minLength":1},
        "nonGoals":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string","minLength":1}},
        "scope":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string","minLength":1}},
        "constraints":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string","minLength":1}},
        "acceptanceCriteria":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string","minLength":1}},
        "projectId":{"type":["string","null"]},
        "agentId":{"type":"string","minLength":1},
        "attachmentIds":{"type":"array","items":{"type":"string","minLength":1}}
      }
    }]}
  }
}`

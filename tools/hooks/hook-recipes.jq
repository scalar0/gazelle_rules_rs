# Only root recipes declare Git hooks. Module recipes do not participate.
[
  .recipes
  | to_entries[]
  | select(any(.value.attributes[]?; .group? == "hooks"))
  | .key
]
| sort
| if all(.[]; test("^[a-z][a-z0-9]*(-[a-z0-9]+)*$")) then
    .[]
  else
    error("Hook names must contain lowercase words separated by hyphens.")
  end

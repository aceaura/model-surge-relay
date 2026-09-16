-- least_used：按累计请求数升序，把最闲的目标排在最前。
-- 计数相同时退回配置顺序，保证同一输入永远得到同一结果。
local entries = {}
local order = 0
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    order = order + 1
    local state = input.runtime[member.model_id] or {}
    table.insert(entries, {
      id = member.model_id,
      used = state.request_count or 0,
      order = order,
    })
  end
end

table.sort(entries, function(a, b)
  if a.used ~= b.used then
    return a.used < b.used
  end
  return a.order < b.order
end)

local out = {}
for _, entry in ipairs(entries) do
  table.insert(out, entry.id)
end
return { candidates = out, note = "least used first" }

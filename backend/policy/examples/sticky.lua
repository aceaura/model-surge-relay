-- sticky：偏好上次成功过的目标，其余按配置顺序兜在后面。
-- "上次成功过"用运行态里的 request_count > 0 且当前无连续失败来近似，
-- 因为 relay 不保存"上次选了谁"——那会让同一目标在多副本间产生分歧。
local preferred, rest = {}, {}
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    local state = input.runtime[member.model_id] or {}
    local healthy = (state.request_count or 0) > 0 and (state.consecutive_failures or 0) == 0
    if healthy then
      table.insert(preferred, member.model_id)
    else
      table.insert(rest, member.model_id)
    end
  end
end
for _, id in ipairs(rest) do
  table.insert(preferred, id)
end
return { candidates = preferred, note = "sticky to previously successful targets" }

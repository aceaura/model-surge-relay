-- preset：按配置顺序取第一个可用目标。
-- 等价于老 replay 的 preset 策略：不做任何动态判断，纯粹按运维者排好的顺序走。
-- 服务侧还会过滤掉未启用与冷却中的目标，所以这里只负责给出顺序。
local out = {}
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    table.insert(out, member.model_id)
  end
end
return { candidates = out, note = "configured order" }

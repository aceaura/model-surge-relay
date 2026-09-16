-- failover：先把首个组内的成员全试完，再跨到下一组。
-- 组已按 position 排好，所以"跨组回退"就是保持组边界的顺序展平；
-- 与 preset 的差别在于 note 声明了意图，且这里显式跳过目录中已消失的引用。
local out = {}
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    if member.known then
      table.insert(out, member.model_id)
    end
  end
end
return { candidates = out, note = "group-by-group failover" }

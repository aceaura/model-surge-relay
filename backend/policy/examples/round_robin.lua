-- round_robin：按请求标识散列轮转起点，其余候选按原顺序接在后面。
-- 用请求标识而非共享游标，是为了让多副本各自算出同一结果而无需协调状态。
local ids = {}
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    table.insert(ids, member.model_id)
  end
end
if #ids == 0 then
  return { candidates = {}, note = "empty collection" }
end

local hash = 0
for i = 1, #(input.request.request_id or "") do
  hash = (hash * 31 + string.byte(input.request.request_id, i)) % 2147483647
end
local start = hash % #ids

local out = {}
for i = 0, #ids - 1 do
  table.insert(out, ids[((start + i) % #ids) + 1])
end
return { candidates = out, note = "round robin from offset " .. start }

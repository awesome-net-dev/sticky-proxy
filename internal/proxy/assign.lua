-- Assignment-table routing: atomic assign-or-return via Redis hash.
-- KEYS[1] = "assignments"
-- KEYS[2] = "backends:active"
-- KEYS[3] = "assignment:counts"
-- ARGV[1] = routingKey
-- ARGV[2] = ISO timestamp (assigned_at)

-- 1. Check existing assignment.
local existing = redis.call("HGET", KEYS[1], ARGV[1])
if existing then
    -- Verify the assigned backend is still active.
    local data = cjson.decode(existing)
    if redis.call("SISMEMBER", KEYS[2], data.backend) == 1 then
        return existing
    end
    -- Backend no longer active; remove stale assignment (decrement by weight).
    local stale_weight = data.weight or 1
    if stale_weight <= 0 then stale_weight = 1 end
    redis.call("HDEL", KEYS[1], ARGV[1])
    redis.call("HINCRBY", KEYS[3], data.backend, -stale_weight)
end

-- 2. Get active backends.
local backends = redis.call("SMEMBERS", KEYS[2])
if #backends == 0 then
    return nil
end

-- 3. Find least-loaded backend using the counts hash.
local minCount = math.huge
local selected = backends[1]
for _, b in ipairs(backends) do
    local c = tonumber(redis.call("HGET", KEYS[3], b) or "0") or 0
    if c < minCount then
        minCount = c
        selected = b
    end
end

-- 4. Create assignment and update count (default weight=1 for on-demand assignment).
local value = cjson.encode({backend=selected, assigned_at=ARGV[2], source="assignment", weight=1})
redis.call("HSET", KEYS[1], ARGV[1], value)
redis.call("HINCRBY", KEYS[3], selected, 1)
return value

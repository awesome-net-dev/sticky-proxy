-- KEYS[1] = sticky:user_id
-- KEYS[2] = backends:active
-- ARGV[1] = ttl
-- ARGV[2] = hash

local existing = redis.call("GET", KEYS[1])
if existing then
    return existing
end

local backends = redis.call("SMEMBERS", KEYS[2])
if #backends == 0 then
    return nil
end

local index = (tonumber(ARGV[2]) % #backends) + 1
local backend = backends[index]

redis.call("SET", KEYS[1], backend, "NX", "EX", ARGV[1])
return backend

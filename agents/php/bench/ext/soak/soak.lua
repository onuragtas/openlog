-- wrk request mix for the soak test (bench/ext/soak/run.sh): Laravel bench queries, an uncaught exception, a
-- reported exception, the health route and, every 200th request, the slow report (function-level trace).
local paths = {}
for i = 1, 40 do
  paths[#paths + 1] = "/bench/" .. i
end
paths[#paths + 1] = "/boom"
paths[#paths + 1] = "/reported"
paths[#paths + 1] = "/health"

local n = 0
request = function()
  n = n + 1
  local p = paths[(n % #paths) + 1]
  if n % 200 == 0 then
    p = "/slow/report?customers=2"
  end
  return wrk.format("GET", p)
end

-- Agent bridge: renders a few seconds for the agent to listen to, and adds
-- or removes installed effects, which REAPER's OSC surface cannot do.
-- REAPER runs Scripts/__startup.lua at launch; this polls one command
-- file, runs render/list/add/remove only, and writes one reply file.
-- Adding and removing need the user's consent file; every change is one
-- REAPER undo step.
local home = os.getenv("HOME") or os.getenv("USERPROFILE") or ""
home = home:gsub("\\", "/")
if home:sub(-1) == "/" then home = home:sub(1, -2) end
local dir = home .. "/.tonelab-muse/bridge/"
-- What may be added is whatever REAPER reports as installed (effects, not
-- instruments): name -> ident, read once per session.
local installed
local function effects()
  if installed then return installed end
  installed = {}
  local i = 0
  while true do
    local ok, name, ident = reaper.EnumInstalledFX(i)
    if not ok then break end
    if not name:match("^%a+i:") then installed[name] = ident end -- "VSTi:", "AUi:"... are instruments
    i = i + 1
  end
  return installed
end
local last = 0
local undoGroup = nil
local function beginEdit()
  if not undoGroup then reaper.Undo_BeginBlock() end
end
local function endEdit(label)
  if not undoGroup then reaper.Undo_EndBlock(label, -1) end
end

local function reply(id, ok, a, b)
  local tmp = dir .. "reply.tmp"
  local f = io.open(tmp, "w")
  if not f then return end
  f:write(id .. "\t" .. (ok and "ok" or "err") .. "\t" .. tostring(a or "") .. "\t" .. tostring(b or "") .. "\n")
  f:close()
  os.rename(tmp, dir .. "reply.txt")
end

-- the user's consent: without this file the bridge does not mutate plugins
local function allowedByUser()
  local f = io.open(dir .. "enabled", "r")
  if f then f:close() return true end
  return false
end

-- render writes the first seconds of the project as REAPER plays it (the
-- agent solos the track first) to path, faster than real time, and puts
-- the project's own render settings back.
local function render(path, seconds, explicitStart)
  local folder, file = path:match("^(.*)[/\\]([^/\\]+)%.wav$")
  if not folder or seconds <= 0 then return nil,"bad render request" end
  local strs = { RENDER_FILE = folder, RENDER_PATTERN = file, RENDER_FORMAT = "ZXZhdxgAAQ==" } -- WAV, 24 bit

  local startPos = explicitStart or 0
  local numItems = reaper.CountMediaItems(0)
  if not explicitStart and numItems > 0 then
    local coversZero = false
    local earliest = nil
    for i = 0, numItems - 1 do
      local it = reaper.GetMediaItem(0, i)
      if it then
        local pos = reaper.GetMediaItemInfo_Value(it, "D_POSITION")
        local len = reaper.GetMediaItemInfo_Value(it, "D_LENGTH")
        if pos <= 0.0 and (pos + len) > 0.0 then
          coversZero = true
          break
        end
        if earliest == nil or pos < earliest then
          earliest = pos
        end
      end
    end
    if not coversZero and earliest ~= nil and earliest > 0 then
      startPos = earliest
    end
  end

  local nums = { RENDER_SETTINGS = 0, RENDER_BOUNDSFLAG = 0, RENDER_STARTPOS = startPos, RENDER_ENDPOS = startPos + seconds,
    RENDER_SRATE = 48000, RENDER_CHANNELS = 2, RENDER_ADDTOPROJ = 0 }
  local old = {}
  for k in pairs(strs) do local _,cur=reaper.GetSetProjectInfo_String(0,k,"",false);old[k]=cur end
  for k in pairs(nums) do old[k]=reaper.GetSetProjectInfo(0,k,0,false) end
  local ok,err=pcall(function()
    for k,v in pairs(strs) do reaper.GetSetProjectInfo_String(0,k,v,true) end
    for k,v in pairs(nums) do reaper.GetSetProjectInfo(0,k,v,true) end
    os.remove(path)
    reaper.Main_OnCommand(42230,0)
  end)
  local failures={}
  if not ok then failures[#failures+1]=tostring(err) end
  for k in pairs(strs) do local good,why=pcall(reaper.GetSetProjectInfo_String,0,k,old[k],true);if not good then failures[#failures+1]=tostring(why) end end
  for k in pairs(nums) do local good,why=pcall(reaper.GetSetProjectInfo,0,k,old[k],true);if not good then failures[#failures+1]=tostring(why) end end
  if #failures>0 then return nil,table.concat(failures,"; ") end
  local f=io.open(path,"rb")
  if not f then return nil,"DAW did not write the render" end
  f:close()
  return path

end

-- starter gives an empty project something to play: a track with the
-- built-in riff at path, as one undo step. A project with any audio in it
-- is left alone.
local function starter(id, path)
  if reaper.CountMediaItems(0) > 0 then return reply(id, true, "kept", "") end
  beginEdit()
  reaper.InsertTrackAtIndex(0, true)
  local tr = reaper.GetTrack(0, 0)
  reaper.GetSetMediaTrackInfo_String(tr, "P_NAME", "Guitar", true)
  reaper.SetOnlyTrackSelected(tr)
  reaper.SetEditCurPos(0, false, false)
  local ok = reaper.InsertMedia(path, 0)
  reaper.SetEditCurPos(0, false, false)
  endEdit("Agent: starter guitar track")
  if ok ~= 1 or reaper.CountMediaItems(0) == 0 then return reply(id, false, "REAPER could not load " .. path) end
  reply(id, true, "created", "")
end

-- readout extracts a numeric value from display text
local function readout(s)
  local t = string.lower(s or "")
  if t:find("^-inf") then return -1e9 end
  if t:find("^inf") or t:find("^+inf") then return 1e9 end
  local num, unit = t:match("([%+%-]?%d*%.?%d+)%s*(%a*)")
  if not num or num == "" or num == "+" or num == "-" then return nil end
  local f = tonumber(num)
  if not f then return nil end
  if unit == "k" or unit == "khz" then
    f = f * 1000
  elseif unit == "ms" then
    f = f / 1000
  end
  return f
end

-- Illinois false-position search inside a single tick
local function findValue(tr, fx, param, target)
  local orig = reaper.TrackFX_GetParamNormalized(tr, fx, param)
  local want = readout(target)

  local function at(x)
    reaper.TrackFX_SetParamNormalized(tr, fx, param, x)
    local ok, text = reaper.TrackFX_GetFormattedParamValue(tr, fx, param, "")
    return text or "", readout(text or "")
  end

  if not want then
    local bestX = 0.0
    local targetLower = string.lower(target)
    for i = 0, 64 do
      local x = i / 64.0
      local text, _ = at(x)
      if string.lower(text):find(targetLower, 1, true) then
        bestX = x
        break
      end
    end
    reaper.TrackFX_SetParamNormalized(tr, fx, param, orig)
    return bestX
  end

  local textLo, flo = at(0.0)
  local textHi, fhi = at(1.0)
  if not flo or not fhi then
    reaper.TrackFX_SetParamNormalized(tr, fx, param, orig)
    return orig
  end

  local lo, hi = 0.0, 1.0
  local best, bestErr = lo, math.abs(flo - want)
  if math.abs(fhi - want) < bestErr then
    best, bestErr = hi, math.abs(fhi - want)
  end

  local function scale(f)
    if math.min(flo, fhi) > 0 and want > 0 and math.max(flo, fhi) / math.min(flo, fhi) > 10 and math.max(flo, fhi) < 1e8 then
      return math.log(f)
    end
    return f
  end

  local glo = scale(flo) - scale(want)
  local ghi = scale(fhi) - scale(want)
  local side = 0

  for i = 1, 12 do
    if bestErr <= 1e-5 then break end
    local x = (lo + hi) / 2
    if math.abs(glo) < 1e8 and math.abs(ghi) < 1e8 and glo ~= ghi then
      x = lo - glo * (hi - lo) / (ghi - glo)
      local m = 0.01 * (hi - lo)
      x = math.min(math.max(x, lo + m), hi - m)
    end
    local curText, f = at(x)
    if f then
      local err = math.abs(f - want)
      if err < bestErr then
        best, bestErr = x, err
      end
      local g = f - want
      if math.min(flo, fhi) > 0 and f > 0 then
        g = scale(f) - scale(want)
      end
      if (g < 0) == (glo < 0) then
        lo, glo = x, g
        if side == -1 then ghi = ghi / 2 end
        side = -1
      else
        hi, ghi = x, g
        if side == 1 then glo = glo / 2 end
        side = 1
      end
    else
      break
    end
  end

  reaper.TrackFX_SetParamNormalized(tr, fx, param, orig)
  return math.floor(best * 10000 + 0.5) / 10000
end

local function findFXBounds(trackChunk, targetFX)
  local lines = {}
  for line in (trackChunk .. "\n"):gmatch("([^\r\n]*)[\r\n]") do
    table.insert(lines, line)
  end
  local inChain = false
  local currentFX = -1
  local depth = 0
  local fxStart = nil
  local fxEnd = nil

  for idx, line in ipairs(lines) do
    local trimmed = line:match("^%s*(.-)%s*$")
    if not inChain then
      if trimmed:find("^<FXCHAIN") then
        inChain = true
      end
    else
      if depth == 0 then
        if trimmed:find("^<") and not trimmed:find("^<FXCHAIN") then
          currentFX = currentFX + 1
          depth = 1
          if currentFX == targetFX then
            fxStart = idx
          end
        elseif trimmed == ">" then
          break
        end
      else
        if trimmed:find("^<") then
          depth = depth + 1
        end
        if trimmed == ">" or trimmed:find(">$") then
          depth = depth - 1
          if depth == 0 then
            if currentFX == targetFX then
              fxEnd = idx
              break
            end
          end
        end
      end
    end
  end
  return lines, fxStart, fxEnd
end

local function extractFXChunk(trackChunk, targetFX)
  local lines, fxStart, fxEnd = findFXBounds(trackChunk, targetFX)
  if not fxStart or not fxEnd then return nil end
  local out = {}
  for i = fxStart, fxEnd do
    table.insert(out, lines[i])
  end
  return table.concat(out, "\n")
end

local function replaceFXChunk(trackChunk, targetFX, newChunk)
  local lines, fxStart, fxEnd = findFXBounds(trackChunk, targetFX)
  if not fxStart or not fxEnd then return nil end
  local out = {}
  for i = 1, fxStart - 1 do
    table.insert(out, lines[i])
  end
  table.insert(out, newChunk)
  for i = fxEnd + 1, #lines do
    table.insert(out, lines[i])
  end
  return table.concat(out, "\n")
end

-- A state line is one unbroken base64 run: the plugin's saved state.
local function isStateLine(s)
  return #s >= 8 and s:match("^[A-Za-z0-9+/=]+$") ~= nil
end

-- reinstantiateChunk swaps the saved state of the FX block at targetFX
-- (0-based) for state (base64 lines) and gives the block a fresh FXID.
-- The block then loads again from the new state when REAPER sets the chunk.
local function reinstantiateChunk(trackChunk, targetFX, state, newGuid)
  local lines, fxStart, fxEnd = findFXBounds(trackChunk, targetFX)
  if not fxStart or not fxEnd then return nil end
  local first, last
  for i = fxStart + 1, fxEnd - 1 do
    if isStateLine(lines[i]:match("^%s*(.-)%s*$")) then
      first = first or i
      last = i
    elseif first then
      break
    end
  end
  local newLines = {}
  for s in (state .. "\n"):gmatch("([^\r\n]*)[\r\n]") do
    local t = s:match("^%s*(.-)%s*$")
    if t ~= "" then table.insert(newLines, t) end
  end
  local out = {}
  for i = 1, #lines do
    local inBlock = i > fxStart and i < fxEnd
    if inBlock and first and i == first then
      for _, s in ipairs(newLines) do table.insert(out, s) end
    end
    if not (inBlock and first and i >= first and i <= last) then
      local l = lines[i]
      if inBlock and newGuid and l:match("^%s*FXID%s") then
        l = l:gsub("FXID%s+%S+", "FXID " .. newGuid, 1)
      end
      table.insert(out, l)
      if i == fxStart and not first then
        for _, s in ipairs(newLines) do table.insert(out, s) end
      end
    end
  end
  local replaced = first and (last - first + 1) or 0
  return table.concat(out, "\n"), replaced, #newLines
end

-- reopenProject loads the project file at path again. REAPER keeps the open
-- project when asked for the path it already has, so a blank project from the
-- bridge folder is loaded first; the old tab's state is dropped, not saved.
local function reopenProject(path)
  local blank = dir .. "blank.rpp"
  local bf = io.open(blank, "rb")
  if bf then
    bf:close()
  else
    local w = io.open(blank, "wb")
    if w then w:write('<REAPER_PROJECT 0.1 "7.79/macOS-arm64" 0\n>\n') w:close() end
  end
  reaper.Main_openProject("noprompt:" .. blank)
  reaper.Main_openProject("noprompt:" .. path)
end

-- patchProjectText writes state into the FX block of a project file (.rpp
-- text) whose FXID is guid. Returns the new text, or nil when no block has it.
local function patchProjectText(text, guid, state)
  local lines = {}
  for line in (text .. "\n"):gmatch("([^\n]*)\n") do
    table.insert(lines, (line:gsub("\r$", "")))
  end
  local target
  for i, l in ipairs(lines) do
    if l:find("FXID", 1, true) and l:find(guid, 1, true) then target = i break end
  end
  if not target then return nil end
  -- the block's closing ">" sits just above the FXID line
  local finish
  for j = target - 1, math.max(1, target - 4), -1 do
    local t = lines[j]:match("^%s*(.-)%s*$")
    if t == ">" then finish = j break end
    if t:sub(1, 1) == "<" then break end
  end
  if not finish then return nil end
  -- and the block's header is the "<" that closes that nesting above it
  local start
  local depth = 0
  for j = finish - 1, 1, -1 do
    local t = lines[j]:match("^%s*(.-)%s*$")
    if t == ">" then
      depth = depth + 1
    elseif t:sub(1, 1) == "<" then
      if depth == 0 then start = j break end
      depth = depth - 1
    end
  end
  if not start then return nil end
  local first, last
  for i = start + 1, finish - 1 do
    if isStateLine(lines[i]:match("^%s*(.-)%s*$")) then
      first = first or i
      last = i
    elseif first then
      break
    end
  end
  local newLines = {}
  for s in (state .. "\n"):gmatch("([^\r\n]*)[\r\n]") do
    local t = s:match("^%s*(.-)%s*$")
    if t ~= "" then table.insert(newLines, t) end
  end
  local out = {}
  for i = 1, #lines do
    if first and i == first then
      for _, s in ipairs(newLines) do table.insert(out, s) end
    end
    if not (first and i >= first and i <= last) then
      table.insert(out, lines[i])
      if i == start and not first then
        for _, s in ipairs(newLines) do table.insert(out, s) end
      end
    end
  end
  return table.concat(out, "\n") .. "\n"
end


-- Only one bounded take can own the input transport. A watchdog still stops
-- and disarms it when the client loses its reply or disappears.
local inputTake
local completedTake
local function finishInput()
  local take = inputTake
  if not take then return end
  inputTake = nil
  local failures = {}
  if take.error then failures[#failures+1] = take.error end
  local function attempt(fn)
    local ok, result = pcall(fn)
    if not ok then failures[#failures+1] = tostring(result) end
    return ok, result
  end
  local _,current = attempt(function() return reaper.EnumProjects(-1) end)
  local selected = attempt(function() reaper.SelectProjectInstance(take.project) end)
  if selected then
    -- Independent attempts ensure one failing API never skips the other
    -- safety operation or leaves the undo block unbalanced.
    attempt(function() reaper.OnStopButton() end)
    attempt(function()
      if not reaper.ValidatePtr2(take.project,take.track,"MediaTrack*") then error("recording track disappeared") end
      reaper.SetMediaTrackInfo_Value(take.track,"I_RECARM",0)
    end)
    -- Retry each safety operation if its first attempt failed or did not stick.
    attempt(function() if reaper.GetPlayState() & 4 ~= 0 then reaper.OnStopButton() end end)
    attempt(function() if reaper.ValidatePtr2(take.project,take.track,"MediaTrack*") and reaper.GetMediaTrackInfo_Value(take.track,"I_RECARM") ~= 0 then reaper.SetMediaTrackInfo_Value(take.track,"I_RECARM",0) end end)
    attempt(function()
      if reaper.GetPlayState() & 4 ~= 0 or reaper.GetMediaTrackInfo_Value(take.track,"I_RECARM") ~= 0 then error("recording stop/disarm was not confirmed") end
      local itemCount = reaper.CountTrackMediaItems(take.track)
      if itemCount < 1 then error("CountTrackMediaItems returned 0 after one-second input recording") end
      local found
      for i = 0, itemCount-1 do
        local item = reaper.GetTrackMediaItem(take.track,i)
        local _,guid = reaper.GetSetMediaItemInfo_String(item,"GUID","",false)
        if not take.items[guid] then
          if found then error("recording produced multiple clips; select one manually") end
          local active = reaper.GetActiveTake(item)
          if not active or reaper.TakeIsMIDI(active) then error("recording did not produce audio") end
          local source = reaper.GetMediaItemTake_Source(active)
          local path = reaper.GetMediaSourceFileName(source)
          if not path or path == "" then error("recording source has no file") end
          found = {path=path,position=reaper.GetMediaItemInfo_Value(item,"D_POSITION"),duration=reaper.GetMediaItemInfo_Value(item,"D_LENGTH")}
        end
      end
      if not found then error("no new input clip was confirmed") end
      take.clip = found
    end)
  end
  attempt(function() reaper.Undo_EndBlock2(take.project,"Agent: record audio input",-1) end)
  if current and current ~= take.project then attempt(function() reaper.SelectProjectInstance(current) end) end
  if #failures>0 then take.error=table.concat(failures,"; ") end
  completedTake = take
end
reaper.atexit(finishInput)

local function run(id, op, track, arg, pos)
  if inputTake and op ~= "record_input_finish" and op ~= "ping" then return reply(id,false,"input recording is active") end
  if op == "record_input_finish" then
    if inputTake and (inputTake.token ~= arg or inputTake.index ~= track) then return reply(id,false,"recording token mismatch") end
    if inputTake then finishInput() end
    local take=completedTake
    if not take or take.token ~= arg or take.index ~= track then return reply(id,false,"no matching recording") end
    if take.error then return reply(id,false,take.error) end
    return reply(id,true,take.clip.path,tostring(take.clip.position)..":"..tostring(take.clip.duration))
  end
  if op == "ping" then return reply(id, true, reaper.GetAppVersion(), "") end
  if op == "begin_undo_action" then
    if undoGroup then
      reaper.Undo_EndBlock(undoGroup, -1)
      undoGroup = nil
    end
    if arg == "" then return reply(id, false, "undo action name is required") end
    undoGroup = arg
    reaper.Undo_BeginBlock()
    return reply(id, true, "started", undoGroup)
  end
  if op == "end_undo_action" then
    local commit, name = arg:match("^([01]):(.*)$")
    if not undoGroup then
      return reply(id, true, commit == "1" and "committed" or "cancelled", name or "")
    end
    local label = undoGroup
    reaper.Undo_EndBlock(label, -1)
    undoGroup = nil
    if commit == "0" then
      if reaper.Undo_CanUndo2(0) == label then
        reaper.Undo_DoUndo2(0)
      end
      return reply(id, true, "cancelled", label)
    end
    return reply(id, true, "committed", label)
  end
  if op == "undo_action_current" then
    local current = reaper.Undo_CanUndo2(0) or ""
    return reply(id, true, current, "")
  end
  if op == "undo" then
    reaper.Undo_DoUndo2(0)
    return reply(id, true, "ok", "")
  end
  -- reopen_project reopens the current project's file without a prompt
  -- (used after the file was put back from a backup)
  if op == "reopen_project" then
    local _, projfn = reaper.EnumProjects(-1)
    if not projfn or projfn == "" then return reply(id, false, "save the project once first") end
    reopenProject(projfn)
    return reply(id, true, "reopened", "")
  end
  if op == "list" then
    local f = io.open(dir .. "catalog.tmp", "w")
    if not f then return reply(id, false, "cannot write the catalog") end
    local n = 0
    for name, ident in pairs(effects()) do f:write(name .. "\t" .. ident .. "\n") n = n + 1 end
    f:close()
    os.rename(dir .. "catalog.tmp", dir .. "catalog.txt")
    return reply(id, true, n, "")
  end
  if op == "render" then local path,err=render(arg,pos/1000);return reply(id,not err,err or path,"") end
  if op == "render_range" then
    local start,path=arg:match("^([^|]+)|(.+)$")
    start=tonumber(start)
    local target=reaper.GetTrack(0,track-1)
    if not start or start<0 or not target then return reply(id,false,"invalid recording range") end
    local keep={}
    for i=0,reaper.CountTracks(0)-1 do
      local tr=reaper.GetTrack(0,i)
      keep[#keep+1]={track=tr,solo=reaper.GetMediaTrackInfo_Value(tr,"I_SOLO"),mute=reaper.GetMediaTrackInfo_Value(tr,"B_MUTE")}
    end
    local rendered
    local ok,err=pcall(function()
      for _,saved in ipairs(keep) do
        local solo=saved.track==target and 1 or 0
        reaper.SetMediaTrackInfo_Value(saved.track,"I_SOLO",solo)
        if saved.track==target then reaper.SetMediaTrackInfo_Value(saved.track,"B_MUTE",0) end
        if reaper.GetMediaTrackInfo_Value(saved.track,"I_SOLO")~=solo then error("range-render solo was not confirmed") end
      end
      if reaper.GetMediaTrackInfo_Value(target,"B_MUTE")~=0 then error("range-render unmute was not confirmed") end
      local why
      rendered,why=render(path,pos/1000,start)
      if why then error(why) end
    end)
    local failures={}
    if not ok then failures[#failures+1]=tostring(err) end
    for _,saved in ipairs(keep) do
      for key,value in pairs({I_SOLO=saved.solo,B_MUTE=saved.mute}) do
        local good,why=pcall(function()
          reaper.SetMediaTrackInfo_Value(saved.track,key,value)
          if reaper.GetMediaTrackInfo_Value(saved.track,key)~=value then error("range-render track restoration was not confirmed") end
        end)
        if not good then failures[#failures+1]=tostring(why) end
      end
    end
    if #failures>0 then return reply(id,false,table.concat(failures,"; ")) end
    return reply(id,true,rendered,"")
  end
  if op == "starter" then return starter(id, arg) end

  if op == "create_track" then
    local idx = pos > 0 and (pos - 1) or reaper.CountTracks(0)
    beginEdit()
    reaper.InsertTrackAtIndex(idx, true)
    local newTr = reaper.GetTrack(0, idx)
    if newTr and arg ~= "" then
      reaper.GetSetMediaTrackInfo_String(newTr, "P_NAME", arg, true)
    end
    endEdit("Agent: create track " .. arg)
    return reply(id, true, idx + 1, arg)
  end

  if op == "set_transport" then
    local action, bpm_s, pos_s, lstart_s, lend_s = arg:match("^([^:]*):([^:]*):([^:]*):([^:]*):([^:]*)$")
    beginEdit()
    if action == "play" then
      reaper.OnPlayButton()
    elseif action == "stop" then
      reaper.OnStopButton()
    elseif action == "pause" then
      reaper.OnPauseButton()
    end
    local bpm = tonumber(bpm_s)
    if bpm and bpm > 0 then
      reaper.SetCurrentBPM(0, bpm, true)
    end
    local posSec = tonumber(pos_s)
    if posSec and posSec >= 0 then
      reaper.SetEditCurPos(posSec, true, false)
    end
    local lstart = tonumber(lstart_s)
    local lend = tonumber(lend_s)
    if lstart and lend and lend > lstart then
      reaper.GetSet_LoopTimeRange(true, true, lstart, lend, false)
    end
    endEdit("Agent: set transport " .. (action or ""))
    return reply(id, true, "ok", "")
  end

  if op == "transport_state" then -- project-wide: needs no track
    local cursor = reaper.GetCursorPosition()
    local loopStart, loopEnd = reaper.GetSet_LoopTimeRange(false, true, 0, 0, false)
    local num, den = reaper.TimeMap_GetTimeSigAtTime(0, cursor)
    local state = string.format("%.6f:%d:%.6f:%.6f:%.6f:%d:%d",
      reaper.Master_GetTempo(), reaper.GetPlayState() & 1, cursor, loopStart, loopEnd,
      math.floor(num), math.floor(den))
    return reply(id, true, state, "")
  end

  local tr = reaper.GetTrack(0, track - 1)
  if not tr then return reply(id, false, "no track " .. track) end

  if op == "record_input_start" then
    local token, seconds = arg:match("^([^|]+)|(.+)$")
    seconds=tonumber(seconds)
    if not token or not seconds or seconds<0.1 or seconds>120 then return reply(id,false,"invalid recording duration") end
    if reaper.GetPlayState() ~= 0 then return reply(id,false,"stop transport before input recording") end
    if reaper.GetSetRepeat(-1) ~= 0 then return reply(id,false,"disable loop recording first") end
    local input = reaper.GetMediaTrackInfo_Value(tr,"I_RECINPUT")
    if input<0 or input>=4096 then return reply(id,false,"select an audio input on the track first") end
    if reaper.GetMediaTrackInfo_Value(tr,"I_RECMODE") ~= 0 then return reply(id,false,"select normal input recording mode first") end
    for i=0,reaper.CountTracks(0)-1 do
      local other=reaper.GetTrack(0,i)
      if other~=tr and reaper.GetMediaTrackInfo_Value(other,"I_RECARM")~=0 then return reply(id,false,"disarm other tracks before recording") end
    end
    local cursor=reaper.GetCursorPosition()
    local items={}
    for i=0,reaper.CountTrackMediaItems(tr)-1 do
      local item=reaper.GetTrackMediaItem(tr,i)
      local at=reaper.GetMediaItemInfo_Value(item,"D_POSITION")
      local length=reaper.GetMediaItemInfo_Value(item,"D_LENGTH")
      if at < cursor+seconds and at+length > cursor then return reply(id,false,"move the cursor to an empty range before recording") end
      local _,guid=reaper.GetSetMediaItemInfo_String(item,"GUID","",false)
      items[guid]=true
    end
    reaper.Undo_BeginBlock2(0)
    inputTake={token=token,index=track,track=tr,project=reaper.EnumProjects(-1),items=items,untilTime=reaper.time_precise()+seconds}
    completedTake=nil
    reaper.SetMediaTrackInfo_Value(tr,"I_RECARM",1)
    reaper.Main_OnCommand(1013, 0) -- Transport: Record
    if reaper.GetPlayState() & 4 == 0 then finishInput();return reply(id,false,"transport did not start recording") end
    return reply(id,true,token,"")
  end

  if op == "program_change" then
    -- arg is "channel:program". The track listens to the virtual keyboard,
    -- armed and monitored, for 0.3 s; its own input and arm/monitor go back.
    local ch_s, prog_s = (arg or ""):match("^(%d+):(%d+)$")
    local ch = tonumber(ch_s) or 0
    local prog = tonumber(prog_s) or -1
    if ch < 1 or ch > 16 or prog < 0 or prog > 127 then return reply(id, false, "bad program change " .. (arg or "")) end
    local keep = {
      input = reaper.GetMediaTrackInfo_Value(tr, "I_RECINPUT"),
      arm = reaper.GetMediaTrackInfo_Value(tr, "I_RECARM"),
      mon = reaper.GetMediaTrackInfo_Value(tr, "I_RECMON"),
    }
    reaper.SetMediaTrackInfo_Value(tr, "I_RECINPUT", 4096 | (62 << 5))
    reaper.SetMediaTrackInfo_Value(tr, "I_RECARM", 1)
    reaper.SetMediaTrackInfo_Value(tr, "I_RECMON", 1)
    reaper.StuffMIDIMessage(0, 0xC0 | (ch - 1), prog, 0)
    local t0 = reaper.time_precise()
    local function finish()
      if reaper.time_precise() - t0 < 0.3 then return reaper.defer(finish) end
      reaper.SetMediaTrackInfo_Value(tr, "I_RECINPUT", keep.input)
      reaper.SetMediaTrackInfo_Value(tr, "I_RECARM", keep.arm)
      reaper.SetMediaTrackInfo_Value(tr, "I_RECMON", keep.mon)
      reply(id, true, "program " .. prog, "channel " .. ch)
    end
    reaper.defer(finish)
    return
  end

  if op == "reinstantiate" then
    -- arg is a state file in the bridge folder; pos is the 1-based FX index
    local fx = pos
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    local path = (arg or ""):gsub("\\", "/")
    if path:sub(1, #dir) ~= dir or path:find("..", 1, true) then
      return reply(id, false, "state must be in the bridge folder")
    end
    local f = io.open(path, "r")
    if not f then return reply(id, false, "cannot read the state file") end
    local state = f:read("*a") or ""
    f:close()
    if not state:find("%S") then return reply(id, false, "empty state") end
    local _, oldName = reaper.TrackFX_GetFXName(tr, fx - 1, "")
    local ok, str = reaper.GetTrackStateChunk(tr, "", false)
    if not ok or not str then return reply(id, false, "cannot read the track chunk") end
    local newStr, replaced, written = reinstantiateChunk(str, fx - 1, state, reaper.genGuid(""))
    if not newStr then return reply(id, false, "no state block for plugin " .. tostring(fx)) end
    beginEdit()
    reaper.SetTrackStateChunk(tr, newStr, false)
    endEdit("Agent: reload " .. oldName)
    local _, name = reaper.TrackFX_GetFXName(tr, fx - 1, "")
    return reply(id, true, name, "replaced " .. replaced .. " lines with " .. written)
  end

  -- reload_offline and reload_offline_b take the FX offline, write the state
  -- while it is offline, and bring it back online: the path a plugin takes
  -- when REAPER sets it offline and online again. reload_offline replaces the
  -- state in the track chunk (FXID kept); reload_offline_b sets it through
  -- TrackFX_SetNamedConfigParm "vst_chunk".
  if op == "reload_offline" or op == "reload_offline_b" then
    local fx = pos
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    local path = (arg or ""):gsub("\\", "/")
    if path:sub(1, #dir) ~= dir or path:find("..", 1, true) then
      return reply(id, false, "state must be in the bridge folder")
    end
    local f = io.open(path, "r")
    if not f then return reply(id, false, "cannot read the state file") end
    local state = f:read("*a") or ""
    f:close()
    if not state:find("%S") then return reply(id, false, "empty state") end
    local _, oldName = reaper.TrackFX_GetFXName(tr, fx - 1, "")
    local detail = ""
    beginEdit()
    reaper.TrackFX_SetOffline(tr, fx - 1, true)
    local err = nil
    if op == "reload_offline" then
      local ok, str = reaper.GetTrackStateChunk(tr, "", false)
      if not ok or not str then
        err = "cannot read the track chunk"
      else
        local newStr, replaced, written = reinstantiateChunk(str, fx - 1, state, nil)
        if not newStr then
          err = "no state block for plugin " .. tostring(fx)
        else
          reaper.SetTrackStateChunk(tr, newStr, false)
          detail = "replaced " .. replaced .. " lines with " .. written
        end
      end
    else
      local done = reaper.TrackFX_SetNamedConfigParm(tr, fx - 1, "vst_chunk", state)
      if not done then err = "REAPER refused the state while offline" end
    end
    reaper.TrackFX_SetOffline(tr, fx - 1, false)
    endEdit("Agent: reload " .. oldName)
    if err then return reply(id, false, err) end
    local _, name = reaper.TrackFX_GetFXName(tr, fx - 1, "")
    return reply(id, true, name, detail)
  end

  -- reload_project_with_state saves the project, backs its file up into the
  -- bridge folder, writes state into the plugin's FX block in the file (found
  -- by FXID), and reopens the project from it without a prompt.
  if op == "reload_project_with_state" then
    local t0 = reaper.time_precise()
    local fx = pos
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    local path = (arg or ""):gsub("\\", "/")
    if path:sub(1, #dir) ~= dir or path:find("..", 1, true) then
      return reply(id, false, "state must be in the bridge folder")
    end
    local f = io.open(path, "r")
    if not f then return reply(id, false, "cannot read the state file") end
    local state = f:read("*a") or ""
    f:close()
    if not state:find("%S") then return reply(id, false, "empty state") end
    local _, projfn = reaper.EnumProjects(-1)
    if not projfn or projfn == "" then return reply(id, false, "save the project once first") end
    local ok, str = reaper.GetTrackStateChunk(tr, "", false)
    if not ok or not str then return reply(id, false, "cannot read the track chunk") end
    -- REAPER writes the FXID after the plugin block's closing ">"
    local lines, _, fxEnd = findFXBounds(str, fx - 1)
    local guid
    if fxEnd then
      for i = fxEnd + 1, #lines do
        local t = lines[i]:match("^%s*(.-)%s*$")
        if t:sub(1, 1) == "<" then break end
        guid = t:match("^FXID%s+(%S+)")
        if guid then break end
      end
    end
    if not guid then return reply(id, false, "the plugin has no FXID to find it by") end
    reaper.Main_SaveProject(0, false)
    local rf = io.open(projfn, "rb")
    if not rf then return reply(id, false, "cannot read the project file") end
    local text = rf:read("*a") or ""
    rf:close()
    local patched = patchProjectText(text, guid, state)
    if not patched then return reply(id, false, "the plugin's block is not in the project file") end
    local bk = io.open(dir .. "backup-" .. os.date("%Y%m%d-%H%M%S") .. ".rpp", "wb")
    if bk then bk:write(text) bk:close() end
    local wf = io.open(projfn, "wb")
    if not wf then return reply(id, false, "cannot write the project file") end
    wf:write(patched)
    wf:close()
    -- the file must hold exactly what was patched before the project reopens
    local vf = io.open(projfn, "rb")
    local back = vf and vf:read("*a") or ""
    if vf then vf:close() end
    if back ~= patched then return reply(id, false, "the project file did not keep the patch") end
    reopenProject(projfn)
    return reply(id, true, "reopened", string.format("%.2f s", reaper.time_precise() - t0))
  end

  if op == "find_value" then
    local fx_s, p_s, target = arg:match("^(%d+):(%d+):(.*)$")
    local fx = tonumber(fx_s) or 0
    local param = tonumber(p_s) or 0
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx_s)) end
    local val = findValue(tr, fx - 1, param - 1, target)
    return reply(id, true, string.format("%.4f", val), "")
  end

  if op == "presets" then
    local n = tonumber(arg) or 0
    if n < 1 or n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
    local fx = n - 1
    local ok, curName = reaper.TrackFX_GetPreset(tr, fx, "")
    curName = ok and curName or ""
    local curIdx, numPresets = reaper.TrackFX_GetPresetIndex(tr, fx)
    local list = {}
    if numPresets and numPresets > 0 then
      local orig = curIdx
      for i = 0, numPresets - 1 do
        reaper.TrackFX_SetPresetByIndex(tr, fx, i)
        local ok_p, name = reaper.TrackFX_GetPreset(tr, fx, "")
        if ok_p and name and name ~= "" then
          table.insert(list, name)
        end
      end
      if orig and orig >= 0 then
        reaper.TrackFX_SetPresetByIndex(tr, fx, orig)
      end
    end
    return reply(id, true, curName, table.concat(list, "|"))
  end

  if op == "get_chunk" then
    local n = tonumber(arg) or 0
    local chunk = ""
    if n > 0 then
      if n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
      local fx = n - 1
      if reaper.TrackFX_GetNamedConfigParm then
        local ok, c = reaper.TrackFX_GetNamedConfigParm(tr, fx, "vst_chunk")
        if ok and c and #c > 0 then chunk = c end
      end
      if chunk == "" then
        local ok, str = reaper.GetTrackStateChunk(tr, "", false)
        if ok and str then chunk = extractFXChunk(str, fx) or "" end
      end
    else
      local ok, str = reaper.GetTrackStateChunk(tr, "", false)
      if ok and str then chunk = str end
    end
    if chunk == "" then return reply(id, false, "chunk empty or unavailable") end
    local f = io.open(dir .. "chunk_out.txt", "w")
    if not f then return reply(id, false, "cannot write chunk_out.txt") end
    f:write(chunk)
    f:close()
    return reply(id, true, "chunk written", "")
  elseif op == "reload" then
    local scriptPath = reaper.GetResourcePath() .. "/Scripts/tonelab_agent_bridge.lua"
    reply(id, true, "reloaded", "")
    dofile(scriptPath)
    return
  end

  if op == "add" then
    local ident = effects()[arg]
    if not ident then return reply(id, false, "not an installed effect: " .. arg) end
    beginEdit()
    local at = pos > 0 and (-1000 - (pos - 1)) or -1 -- -1 = append
    local i = reaper.TrackFX_AddByName(tr, arg, false, at)
    if i < 0 then i = reaper.TrackFX_AddByName(tr, ident, false, at) end
    endEdit("Agent: add " .. arg)
    if i < 0 then return reply(id, false, "REAPER could not load " .. arg) end
    local _, name = reaper.TrackFX_GetFXName(tr, i, "")
    return reply(id, true, i + 1, name)
  elseif op == "name" then
    local n = tonumber(arg) or 0
    if n < 1 or n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
    local _, name = reaper.TrackFX_GetFXName(tr, n - 1, "")
    return reply(id, true, n, name)
  elseif op == "remove" or op == "force_remove" then
    local n = tonumber(arg) or 0
    if n < 1 or n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
    local _, name = reaper.TrackFX_GetFXName(tr, n - 1, "")
    beginEdit()
    reaper.TrackFX_Delete(tr, n - 1)
    endEdit("Agent: remove " .. name)
    return reply(id, true, n, name)
  elseif op == "move" then
    local from_fx = tonumber(arg) or 0
    local to_fx = pos
    if from_fx < 1 or from_fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
    if to_fx < 1 or to_fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "bad destination " .. to_fx) end
    if from_fx == to_fx then return reply(id, true, to_fx, "") end
    local _, want = reaper.TrackFX_GetFXName(tr, from_fx - 1, "")
    local function at(i)
      local _, nm = reaper.TrackFX_GetFXName(tr, i, "")
      return nm
    end
    beginEdit()
    -- TrackFX_CopyToTrack returns nothing, so the name at the target is the
    -- only proof of the move. REAPER may count the destination before or
    -- after the source is taken out; try the place that matches the name,
    -- then the other one, but only while the source still holds the FX.
    reaper.TrackFX_CopyToTrack(tr, from_fx - 1, tr, to_fx - 1, true)
    if at(to_fx - 1) ~= want and at(from_fx - 1) == want then
      local alt = from_fx < to_fx and to_fx or to_fx - 2
      if alt >= 0 and alt < reaper.TrackFX_GetCount(tr) then
        reaper.TrackFX_CopyToTrack(tr, from_fx - 1, tr, alt, true)
      end
    end
    endEdit("Agent: move FX")
    if at(to_fx - 1) ~= want then return reply(id, false, "move failed") end
    return reply(id, true, to_fx, "")
  elseif op == "duplicate_track" then
    beginEdit()
    reaper.SetOnlyTrackSelected(tr)
    reaper.Main_OnCommand(40062, 0) -- Track: Duplicate tracks
    local newIdx = track
    local newTr = reaper.GetTrack(0, newIdx)
    if newTr and arg ~= "" then
      reaper.GetSetMediaTrackInfo_String(newTr, "P_NAME", arg, true)
    end
    endEdit("Agent: duplicate track " .. track)
    return reply(id, true, newIdx + 1, arg)
  elseif op == "delete_track" then
    beginEdit()
    reaper.DeleteTrack(tr)
    endEdit("Agent: delete track " .. track)
    return reply(id, true, "deleted", "")
  elseif op == "rename_track" then
    beginEdit()
    reaper.GetSetMediaTrackInfo_String(tr, "P_NAME", arg, true)
    endEdit("Agent: rename track")
    return reply(id, true, arg, "")
  elseif op == "insert_media" then
    local posSec = pos / 1000.0
    local before = {}
    for i = 0, reaper.CountTrackMediaItems(tr) - 1 do
      local item = reaper.GetTrackMediaItem(tr, i)
      local _, guid = reaper.GetSetMediaItemInfo_String(item, "GUID", "", false)
      before[guid] = true
    end
    beginEdit()
    reaper.SetOnlyTrackSelected(tr)
    reaper.SetEditCurPos(posSec, false, false)
    local ok = reaper.InsertMedia(arg, 0)
    endEdit("Agent: insert media " .. arg)
    if ok ~= 1 then return reply(id, false, "could not insert media " .. arg) end
    local found, addedItems = -1, {}
    for i = 0, reaper.CountTrackMediaItems(tr) - 1 do
      local item = reaper.GetTrackMediaItem(tr, i)
      local _, guid = reaper.GetSetMediaItemInfo_String(item, "GUID", "", false)
      if not before[guid] then
        addedItems[#addedItems + 1] = item
        found = i + 1
      end
    end
    if #addedItems ~= 1 then
      for _, item in ipairs(addedItems) do reaper.DeleteTrackMediaItem(tr, item) end
      return reply(id, false, "expected one new media item, found " .. tostring(#addedItems))
    end
    return reply(id, true, "inserted", tostring(found))
  elseif op == "delete_media_item" then
    local index = tonumber(arg)
    if not index or index < 1 or index > reaper.CountTrackMediaItems(tr) then return reply(id, false, "invalid media item index") end
    local item = reaper.GetTrackMediaItem(tr, index - 1)
    local ok = item and reaper.DeleteTrackMediaItem(tr, item)
    if not ok then return reply(id, false, "media item deletion failed") end
    return reply(id, true, "deleted", "")
  elseif op == "delete_media_path" then
    local removed = 0
    for i = reaper.CountTrackMediaItems(tr) - 1, 0, -1 do
      local item = reaper.GetTrackMediaItem(tr, i)
      local take = item and reaper.GetActiveTake(item)
      local source = take and reaper.GetMediaItemTake_Source(take)
      local sourcePath = source and reaper.GetMediaSourceFileName(source) or ""
      if sourcePath == arg then
        if not reaper.DeleteTrackMediaItem(tr, item) then return reply(id, false, "media item cleanup failed") end
        removed = removed + 1
      end
    end
    return reply(id, true, tostring(removed), "")
  elseif op == "create_midi_riff" then
    local startPos = tonumber(arg) or 0
    local item = reaper.CreateNewMIDIItemInProj(tr, startPos, startPos + 2, false)
    if not item then return reply(id, false, "MIDI item creation failed") end
    local take = reaper.GetActiveTake(item)
    if not take then reaper.DeleteTrackMediaItem(tr, item); return reply(id, false, "MIDI take creation failed") end
    local ok = reaper.MIDI_InsertNote(take, false, false, 0, 960, 0, 60, 96, false)
    if not ok then reaper.DeleteTrackMediaItem(tr, item); return reply(id, false, "MIDI note insertion failed") end
    reaper.MIDI_Sort(take)
    for i = 0, reaper.CountTrackMediaItems(tr) - 1 do
      if reaper.GetTrackMediaItem(tr, i) == item then return reply(id, true, tostring(i + 1), "") end
    end
    reaper.DeleteTrackMediaItem(tr, item)
    return reply(id, false, "created MIDI item could not be identified")
  elseif op == "fx_ui_state" then
    local fx = tonumber(arg) or 0
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
    return reply(id, true, reaper.TrackFX_GetOpen(tr, fx - 1) and "1" or "0", "")
  elseif op == "set_fx_ui" then
    local fx, visible = arg:match("^(%d+):([01])$")
    fx = tonumber(fx) or 0
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    reaper.TrackFX_Show(tr, fx - 1, visible == "1" and 3 or 2)
    return reply(id, true, reaper.TrackFX_GetOpen(tr, fx - 1) and "1" or "0", "")
  elseif op == "create_send" then
    local dst_s, db_s = arg:match("^(%d+):(.*)$")
    local dstIdx = tonumber(dst_s) or 0
    local db = tonumber(db_s) or 0.0
    local dstTr = reaper.GetTrack(0, dstIdx - 1)
    if not dstTr then return reply(id, false, "no destination track " .. dst_s) end
    beginEdit()
    local sendIdx = reaper.CreateTrackSend(tr, dstTr)
    if sendIdx >= 0 then
      local vol = 10.0 ^ (db / 20.0)
      reaper.SetTrackSendInfo_Value(tr, 0, sendIdx, "D_VOL", vol)
    end
    endEdit("Agent: create send")
    if sendIdx < 0 then return reply(id, false, "failed to create send") end
    return reply(id, true, sendIdx + 1, "")
  elseif op == "remove_send" then
    local sendIdx = (tonumber(arg) or 0) - 1
    if sendIdx < 0 or sendIdx >= reaper.GetTrackNumSends(tr, 0) then return reply(id, false, "no send at that index") end
    reaper.Undo_BeginBlock()
    reaper.RemoveTrackSend(tr, 0, sendIdx)
    reaper.Undo_EndBlock("Agent: remove send", -1)
    if sendIdx < reaper.GetTrackNumSends(tr, 0) then return reply(id, false, "send removal was not confirmed") end
    return reply(id, true, "removed", "")
  elseif op == "track_control" then
    local control, value = arg:match("^([^:]+):(.*)$")
    if control == "arm" then
      reaper.SetMediaTrackInfo_Value(tr, "I_RECARM", tonumber(value) or 0)
    elseif control == "monitor" then
      reaper.SetMediaTrackInfo_Value(tr, "I_RECMON", tonumber(value) or 0)
    elseif control == "input" then
      reaper.SetMediaTrackInfo_Value(tr, "I_RECINPUT", tonumber(value) or -1)
    elseif control == "volume" then
      reaper.SetMediaTrackInfo_Value(tr, "D_VOL", tonumber(value) or 1)
    elseif control == "pan" then
      reaper.SetMediaTrackInfo_Value(tr, "D_PAN", tonumber(value) or 0)
    elseif control == "mute" then
      reaper.SetMediaTrackInfo_Value(tr, "B_MUTE", tonumber(value) or 0)
    elseif control == "solo" then
      reaper.SetMediaTrackInfo_Value(tr, "I_SOLO", tonumber(value) or 0)
    else return reply(id, false, "unknown track control") end
    local key = ({arm="I_RECARM", monitor="I_RECMON", input="I_RECINPUT", volume="D_VOL", pan="D_PAN", mute="B_MUTE", solo="I_SOLO"})[control]
    return reply(id, true, reaper.GetMediaTrackInfo_Value(tr, key), "")
  elseif op == "track_state" then
    local function v(key) return math.floor(reaper.GetMediaTrackInfo_Value(tr, key)) end
    local state = string.format("%d:%d:%d:%d:%d:%.6f:%.6f:%d",
      v("I_RECARM"), v("B_MUTE"), v("I_SOLO"), v("I_RECMON"), v("I_RECINPUT"),
      reaper.GetMediaTrackInfo_Value(tr, "D_VOL"), reaper.GetMediaTrackInfo_Value(tr, "D_PAN"),
      reaper.CountTrackMediaItems(tr))
    return reply(id, true, state, "")
  elseif op == "send_list" then
    local rows = {}
    for i = 0, reaper.GetTrackNumSends(tr, 0) - 1 do
      local dest = reaper.GetTrackSendInfo_Value(tr, 0, i, "P_DESTTRACK")
      local destIdx = dest and math.floor(reaper.GetMediaTrackInfo_Value(dest, "IP_TRACKNUMBER")) or 0
      rows[#rows + 1] = string.format("%d:%.6f", destIdx, reaper.GetTrackSendInfo_Value(tr, 0, i, "D_VOL"))
    end
    return reply(id, true, table.concat(rows, "|"), "")
  elseif op == "fx_wet" then
    local fx = pos
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    local p = reaper.TrackFX_GetParamFromIdent(tr, fx - 1, ":wet")
    if p < 0 then return reply(id, false, "plugin has no wet control") end
    if arg ~= "get" then
      local value = tonumber(arg)
      if not value or value < 0 or value > 1 then return reply(id, false, "wet must be get or a number from 0 to 1") end
      reaper.TrackFX_SetParam(tr, fx - 1, p, value)
    end
    return reply(id, true, string.format("%.6f", reaper.TrackFX_GetParam(tr, fx - 1, p)), "")
  elseif op == "param_readout" then
    local fx = pos
    if fx < 1 or fx > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx)) end
    local p = tonumber(arg) or -1
    if p < 0 or p >= reaper.TrackFX_GetNumParams(tr, fx - 1) then return reply(id, false, "no parameter " .. arg) end
    local _, text = reaper.TrackFX_GetFormattedParamValue(tr, fx - 1, p, "")
    if not text or text == "" then
      local _, formatted = reaper.TrackFX_FormatParamValue(tr, fx - 1, p, reaper.TrackFX_GetParam(tr, fx - 1, p), "")
      text = formatted
    end
    return reply(id, true, text or "", "")
  elseif op == "list_markers" then
    local rows = {}
    local _, markerCount, regionCount = reaper.CountProjectMarkers(0)
    local count = markerCount + regionCount
    for i = 0, count - 1 do
      local ok, isRegion, pos, finish, name, markIdx = reaper.EnumProjectMarkers(i)
      if ok then table.insert(rows, table.concat({i + 1, (name or ""):gsub("[:|]", " "), pos, finish, isRegion and "1" or "0"}, ":")) end
    end
    return reply(id, true, count, table.concat(rows, "|"))
  elseif op == "create_marker" then
    local startS, endS, isRegionS, name = arg:match("^([^:]+):([^:]+):([^:]+):(.*)$")
    local startPos, endPos = tonumber(startS), tonumber(endS)
    if not startPos then return reply(id, false, "invalid marker position") end
    reaper.Undo_BeginBlock()
    local idx = reaper.AddProjectMarker2(0, isRegionS == "1", startPos, endPos or startPos, name, -1, 0)
    reaper.Undo_EndBlock("Agent: create marker", -1)
    if idx < 0 then return reply(id, false, "marker creation failed") end
    local _, totalMarks, totalRegions = reaper.CountProjectMarkers(0)
    local count = totalMarks + totalRegions
    for i = 0, count - 1 do
      local ok, isRegion, pos, finish, gotName = reaper.EnumProjectMarkers(i)
      if ok and math.abs(pos - startPos) <= 0.001 and isRegion == (isRegionS == "1") and gotName == name and (not isRegion or math.abs(finish - (endPos or startPos)) <= 0.001) then
        return reply(id, true, i + 1, "")
      end
    end
    return reply(id, false, "marker readback did not match")
  elseif op == "delete_marker" then
    local index = tonumber(arg)
    if not index then return reply(id, false, "invalid marker index") end
    local ok = reaper.DeleteProjectMarker(0, index, false)
    if not ok then ok = reaper.DeleteProjectMarker(0, index, true) end
    if not ok then return reply(id, false, "marker deletion failed") end
    return reply(id, true, "deleted", "")
  elseif op == "goto_marker" then
    local index = (tonumber(arg) or 0) - 1
    local ok, _, pos = reaper.EnumProjectMarkers(index)
    if not ok then return reply(id, false, "no marker at that index") end
    reaper.SetEditCurPos(pos, true, false)
    if math.abs(reaper.GetCursorPosition() - pos) > 0.001 then return reply(id, false, "cursor readback failed") end
    return reply(id, true, pos, "")
  elseif op == "set_timesig" then
    local numerator, denominator = arg:match("^(%d+):(%d+)$")
    numerator, denominator = tonumber(numerator), tonumber(denominator)
    if not numerator or not denominator then return reply(id, false, "invalid time signature") end
    reaper.Undo_BeginBlock()
    reaper.SetTempoTimeSigMarker(0, -1, 0, -1, -1, reaper.Master_GetTempo(), numerator, denominator, true)
    reaper.Undo_EndBlock("Agent: set time signature", -1)
    local _, gotNum, gotDen = reaper.TimeMap_GetTimeSigAtTime(0, 0)
    if gotNum ~= numerator or gotDen ~= denominator then return reply(id, false, "time signature readback did not match") end
    return reply(id, true, numerator .. "/" .. denominator, "")
  elseif op == "save_project" then
    local _, projectPath = reaper.EnumProjects(-1)
    if not projectPath or projectPath == "" then return reply(id, false, "save the project once first") end
    reaper.Main_SaveProject(0, false)
    local f = io.open(projectPath, "rb")
    if not f then return reply(id, false, "saved project file is not readable") end
    f:close()
    return reply(id, true, projectPath, "")
  elseif op == "set_preset" then
    local fx_s, preset = arg:match("^(%d+):(.*)$")
    local n = tonumber(fx_s) or 0
    if n < 1 or n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. tostring(fx_s)) end
    local fx = n - 1
    beginEdit()
    local ok = reaper.TrackFX_SetPreset(tr, fx, preset)
    endEdit("Agent: set preset " .. preset)
    if not ok then return reply(id, false, "could not set preset " .. preset) end
    return reply(id, true, preset, "")
  elseif op == "set_chunk" then
    local n = tonumber(arg) or 0
    local f = io.open(dir .. "chunk_in.txt", "r")
    if not f then return reply(id, false, "cannot read chunk_in.txt") end
    local chunk = f:read("*a")
    f:close()
    if not chunk or #chunk == 0 then return reply(id, false, "empty chunk in chunk_in.txt") end
    if n > 0 then
      if n > reaper.TrackFX_GetCount(tr) then return reply(id, false, "no plugin " .. arg) end
      local fx = n - 1
      local wasOpen = reaper.TrackFX_GetOpen(tr, fx)
      local done = false
      if reaper.TrackFX_SetNamedConfigParm then
        done = reaper.TrackFX_SetNamedConfigParm(tr, fx, "vst_chunk", chunk)
      end
      if not done then
        local ok, str = reaper.GetTrackStateChunk(tr, "", false)
        if ok and str then
          local newStr = replaceFXChunk(str, fx, chunk)
          if newStr then
            beginEdit()
            reaper.SetTrackStateChunk(tr, newStr, false)
            endEdit("Agent: set FX chunk")
            done = true
          end
        end
      end
      if not done then return reply(id, false, "could not set FX chunk") end
      if wasOpen then
        reaper.TrackFX_SetOpen(tr, fx, false)
        reaper.TrackFX_SetOpen(tr, fx, true)
      end
    else
      beginEdit()
      reaper.SetTrackStateChunk(tr, chunk, false)
      endEdit("Agent: set track chunk")
    end
    return reply(id, true, "ok", "")
  end
  reply(id, false, "unknown op " .. op)
end

local resPath = (reaper.GetResourcePath() or ""):gsub("\\", "/")
local scriptPath = resPath .. "/Scripts/tonelab_agent_bridge.lua"
local function readBridgeScript()
  local f = io.open(scriptPath, "rb")
  if not f then return nil end
  local content = f:read("*a")
  f:close()
  return content
end
local loadedContent = readBridgeScript()
local lastReloadCheck = 0

local function poll()
  local now = reaper.time_precise()
  if inputTake then
    local ok,reason=pcall(function()
      if reaper.EnumProjects(-1) ~= inputTake.project then return "project changed during input recording" end
      for i=0,reaper.CountTracks(0)-1 do
        local other=reaper.GetTrack(0,i)
        if other~=inputTake.track and reaper.GetMediaTrackInfo_Value(other,"I_RECARM")~=0 then return "another track was armed during input recording; transport stopped" end
      end
    end)
    if not ok or reason then inputTake.error=tostring(reason);finishInput()
    elseif now>=inputTake.untilTime then finishInput() end
  end
  if not inputTake and now - lastReloadCheck > 1.0 then
    lastReloadCheck = now
    local curContent = readBridgeScript()
    if curContent and curContent ~= "" then
      if loadedContent and curContent ~= loadedContent then
        loadedContent = curContent
        dofile(scriptPath)
        return
      elseif not loadedContent then
        loadedContent = curContent
      end
    end
  end
  if now - last > 0.1 then
    last = now
    local f = io.open(dir .. "cmd.txt", "r")
    if f then
      local line = f:read("*l") or ""
      f:close()
      os.remove(dir .. "cmd.txt")
      local id, op, track, arg, pos = line:match("^([^\t]*)\t([^\t]*)\t([^\t]*)\t([^\t]*)\t([^\t]*)$")
      if id then
        local ok, err = pcall(run, id, op, tonumber(track) or 0, arg or "", tonumber(pos) or 0)
        if not ok then
          reply(id, false, "lua error: " .. tostring(err))
        end
      end
    end
  end
  reaper.defer(poll)
end

poll()

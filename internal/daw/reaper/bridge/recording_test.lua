-- Executes the actual bridge against a fake API, with no DAW or audio device.
local script, scenario=arg[1],arg[2]
local home=os.getenv("HOME")
local dir=home.."/.tonelab-muse/bridge/"
local now=1
local queued
local exit
local state=0
local began,ended=0,0
local project={}
local otherProject={}
local activeProject=project
local renderStrings={}
local renderNumbers={}
local tracks={{arm=0,input=0,mode=0,solo=0,mute=1,items={}},{arm=0,input=0,mode=0,solo=2,mute=0,items={}}}
local start=0
local failStop=scenario=="stop-failure"
local failDisarm=scenario=="disarm-failure"
reaper={
 GetResourcePath=function() return home end,
 time_precise=function() return now end,
 defer=function(fn) queued=fn end,
 atexit=function(fn) exit=fn end,
 EnumProjects=function() return activeProject,"project" end,
 SelectProjectInstance=function(p) assert(p==project or p==otherProject);activeProject=p end,
 CountTracks=function() return #tracks end,
 GetTrack=function(_,i) return tracks[i+1] end,
 GetPlayState=function() return state end,
 GetSetRepeat=function() return 0 end,
 GetCursorPosition=function() return 12 end,
 GetMediaTrackInfo_Value=function(t,key) if key=="I_SOLO" then return t.solo elseif key=="B_MUTE" then return t.mute elseif key=="I_RECARM" then return t.arm elseif key=="I_RECINPUT" then return t.input elseif key=="I_RECMODE" then return t.mode end;error("unexpected property "..key) end,
 SetMediaTrackInfo_Value=function(t,key,value) if key=="I_SOLO" then t.solo=value;return elseif key=="B_MUTE" then t.mute=value;return end;assert(key=="I_RECARM");if value==0 and failDisarm then failDisarm=false;error("injected disarm failure") end;t.arm=value end,
 CountMediaItems=function() return 1 end,
 GetSetProjectInfo_String=function(_,key,value,set) local old=renderStrings[key] or "original";if set then renderStrings[key]=value end;return true,old end,
 GetSetProjectInfo=function(_,key,value,set) local old=renderNumbers[key] or 0;if set then renderNumbers[key]=value end;return old end,
 Main_OnCommand=function(command)
  assert(command==42230)
  assert(tracks[1].solo==1 and tracks[1].mute==0 and tracks[2].solo==0,"render was not isolated")
  assert(renderNumbers.RENDER_STARTPOS==12 and math.abs(renderNumbers.RENDER_ENDPOS-12.1)<.0001,"wrong clip range")
  if scenario=="range-render-failure" then error("injected render failure") end
  local f=assert(io.open(renderStrings.RENDER_FILE.."/"..renderStrings.RENDER_PATTERN..".wav","w"));f:write("audio");f:close()
 end,
 CountTrackMediaItems=function(t) return #t.items end,
 GetTrackMediaItem=function(t,i) return t.items[i+1] end,
 GetSetMediaItemInfo_String=function(item,key) assert(key=="GUID");return true,item.id end,
 GetMediaItemInfo_Value=function(item,key) if key=="D_POSITION" then return item.position elseif key=="D_LENGTH" then return item.duration end;error("unexpected item field") end,
 GetActiveTake=function(item) return item end,
 TakeIsMIDI=function() return false end,
 GetMediaItemTake_Source=function(item) return item end,
 GetMediaSourceFileName=function(...) assert(select("#",...)==1,"wrong native filename signature");return "new-input.wav" end,
 ValidatePtr2=function(_,t) return t==tracks[1] or t==tracks[2] end,
 Undo_BeginBlock2=function() began=began+1 end,
 Undo_EndBlock2=function() ended=ended+1;if scenario=="undo-failure" then error("injected undo failure") end end,
 OnRecordButton=function() state=5;start=now end,
 OnStopButton=function()
  if failStop then failStop=false;error("injected stop failure") end
  if state & 4 ~= 0 then
   for i,t in ipairs(tracks) do if t.arm~=0 then t.items[#t.items+1]={id="new-"..i,position=12,duration=now-start} end end
  end
  state=0
 end,
}
local function tick(delta) now=now+delta;local fn=queued;queued=nil;assert(fn,"no queued bridge poll");fn() end
local function command(id,op,value,pos)
 local f=assert(io.open(dir.."cmd.txt","w"));f:write(id.."\t"..op.."\t1\t"..value.."\t"..tostring(pos or 0).."\n");f:close();tick(.11)
 local r=assert(io.open(dir.."reply.txt","r"));local reply=r:read("*a");r:close();assert(reply:match("^"..id.."\t"),reply);return reply
end
dofile(script)
if scenario=="other-armed-at-start" then tracks[2].arm=1 end
local first=command("start","record_input_start","token|0.5")
if scenario=="other-armed-at-start" then assert(first:find("\terr\t",1,true));assert(state==0 and began==0 and tracks[1].arm==0);print("ok "..scenario);return end
assert(first:find("\tok\ttoken",1,true),first)
assert(state & 4 ~= 0 and tracks[1].arm==1)
if scenario=="project-change" then activeProject=otherProject;tick(.01)
elseif scenario=="other-armed-mid-take" then tracks[2].arm=1;tick(.01)
elseif scenario=="exit" then tick(.1);exit()
else tick(.6) end
assert(state==0,"transport was not stopped")
assert(tracks[1].arm==0,"target was not disarmed")
assert(began==1 and ended==1,"undo block not balanced")
local reply=command("finish","record_input_finish","token")
local fails=scenario=="project-change" or scenario=="other-armed-mid-take" or scenario=="stop-failure" or scenario=="disarm-failure" or scenario=="undo-failure"
if fails then assert(reply:find("\terr\t",1,true),reply) else assert(reply:find("\tok\tnew-input.wav\t12:",1,true),reply);assert(#tracks[2].items==0,"recorded another track") end
local again=command("again","record_input_finish","token");assert(again:gsub("^again","finish")==reply,"finish was not idempotent")
assert(command("wrong","record_input_finish","unrelated"):find("\terr\t",1,true),"unrelated token accepted")
if scenario=="range-render" or scenario=="range-render-failure" then
 local result=command("render","render_range","12|"..home.."/audition.wav",100)
 if scenario=="range-render" then assert(result:find("\tok\t",1,true),result) else assert(result:find("\terr\t",1,true),result) end
 assert(tracks[1].solo==0 and tracks[1].mute==1 and tracks[2].solo==2 and tracks[2].mute==0,"render changed prior solo/mute state")
 for _,value in pairs(renderStrings) do assert(value=="original","render string setting leaked") end
 for _,value in pairs(renderNumbers) do assert(value==0,"render numeric setting leaked") end
end
if scenario=="project-change" then assert(activeProject==otherProject,"did not restore selected project") end
print("ok "..scenario)

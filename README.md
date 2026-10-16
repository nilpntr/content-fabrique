# Content Fabrique
AI tooling for creators. 

## FYI
I already implemented a base ui design which you can fill to become an actual useable dashboard(the apps/web folder)

## Ideas
Start with trying to automate editing for a fitness creator so finding viral tiktok songs and concepts and person can upload content and the research and editor agent. Also give the ability to a user to specify it's idea but that's optional let's say he says i want to start with my best back pose from the clips i shared with the title how to grow a back 101 and then after that the clip of him/her performing the exercise with a viral song maybe matching the video. But that's optional input the user can give also freedom is important for this software.

## Agents
### Research
Find viral concepts

### Editor
Using ffmpeg and other tooling to edit a viral concept based on creators content

## Links
https://x.com/moritzkremb/status/2109645618482147708
https://www.framejam.ai/

## Dashboard
Create a simple dashboard where people can upload their content, let ai create the ideas, get a simple timeline interface where people can highlight a certain period in the video with remarks/comments

## Tech Stack
- Tanstack Start(apps/web)
- Backend in golang(apps/server)
- Cloudflare
- R2
- Clerk
- K2 for streaming if needed
- D1 temporarily for database but go to planetscale once really ready
- For video editting let is use R2 i guess
- Give agents a sandbox, maybe run the backend on a VM so you can run sandboxes on there or we can run the sandboxes on the vm so you can create a plugable sandbox layer to a hosted one or this one yourself

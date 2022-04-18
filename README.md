# Autoscaler for Resque workers

This code autoscales a Resque worker pool running on [Render](https://render.com) based on the number of unfinished jobs 
(enqueued jobs + in-progress jobs).
It computes this metric directly from the redis instance being used by Resque. 
It assumes that the Resque workers are running as a multi-instance [Render background worker](https://render.com/docs/background-workers).
The autoscaler can itself run as a single-instance Render background worker.
It takes the following config options as environment variables:

- `WORKER_SERVICE_ID` (required): Service ID for the Resque worker pool running as a Render background worker.
- `RENDER_API_KEY`(required): See https://render.com/docs/api for instructions on how to generate an API key.
- `REDIS_ADDRESS` (required): `host:port` for redis server used by Resque. Can be a [Render managed redis](https://render.com/docs/redis) server.
- `MIN_INSTANCES` (optional, defaults to 2): Minimum number of worker instances.
- `MAX_INSTANCES` (optional, defaults to 50): Maximum number of worker instances.
- `WORKERS_PER_INSTANCE` (optional, defaults to 1): Number of Resque workers running on each instance (see https://github.com/resque/resque#running-workers).
- `INTERVAL` (optional, defaults to 1s): Determines how often we sample the custom metric. After each measurement we wait for this amount of time before measuring again.
- `NUM_SAMPLES` (optional, defaults to 1): How many samples to average over when calculating the desired number of worker instances.
- `SCALE_UP_DELAY` (optional, defauls to 1m): Minimum time to wait after the last scaling event before scaling up.
- `SCALE_DOWN_DELAY` (optional, defaults to 10m): Minimum time to wait after the last scaling event before scaling down.

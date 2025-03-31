#! /bin/zsh

curl -svL https://recommend.natwelch.com/cron/cache
sleep 120
curl -svL https://recommend.natwelch.com/cron/recommend

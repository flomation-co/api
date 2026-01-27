FROM alpine

RUN apk add -U --no-cache ca-certificates
COPY dist/ .

EXPOSE 8888
CMD [ "./application" ]
FROM python:3.14-alpine

RUN apk add --no-cache git bash openssh

WORKDIR /app

COPY . .

RUN pip install --no-cache-dir -r requirements.txt

EXPOSE 9000

CMD ["python", "webhook.py"]

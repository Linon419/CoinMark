FROM python:3.10-slim

WORKDIR /app

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY coinmark_api ./coinmark_api

EXPOSE 8000

CMD ["python", "-m", "coinmark_api.main"]

